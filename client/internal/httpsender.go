package internal

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/signing"
)

const (
	OpAMPPlainHTTPMethod     = "POST"
	defaultPollingIntervalMs = 30 * 1000 // default interval is 30 seconds.
)

const (
	headerContentEncoding = "Content-Encoding"
	encodingTypeGZip      = "gzip"
)

type requestWrapper struct {
	*http.Request

	bodyReader func() io.ReadCloser
}

func bodyReader(buf []byte) func() io.ReadCloser {
	return func() io.ReadCloser {
		return io.NopCloser(bytes.NewReader(buf))
	}
}

func (r *requestWrapper) rewind(ctx context.Context) {
	r.Body = r.bodyReader()
	r.Request = r.Request.WithContext(ctx)
}

// HTTPSender allows scheduling messages to send. Once run, it will loop through
// a request/response cycle for each message to send and will process all received
// responses using a receivedProcessor. If there are no pending messages to send
// the HTTPSender will wait for the configured polling interval.
type HTTPSender struct {
	SenderCommon

	url                string
	logger             types.Logger
	client             *http.Client
	callbacks          types.Callbacks
	pollingIntervalMs  atomic.Int64
	compressionEnabled bool
	maxMessageSize     int64

	// Headers to send with all requests.
	getHeader func() http.Header

	// Processor to handle received messages.
	receiveProcessor receivedProcessor

	// attestation, when non-nil, decodes inbound responses as
	// SignedServerToAgent envelopes — validates the trust chain on the
	// first response and verifies the signature on every subsequent
	// one. Set by Run when the StartSettings supplied a PayloadVerifier.
	attestation *attestationState
}

// NewHTTPSender creates a new Sender that uses HTTP to send messages
// with default settings.
func NewHTTPSender(logger types.Logger) *HTTPSender {
	h := &HTTPSender{
		SenderCommon: NewSenderCommon(),
		logger:       logger,
	}
	h.pollingIntervalMs.Store(defaultPollingIntervalMs)
	h.maxMessageSize = internal.DefaultMaxMessageSize
	// initialize the headers with no additional headers
	h.SetRequestHeader(nil, nil)
	return h
}

// SetHTTPClient sets the HTTP client used to send OpAMP requests.
// It must be called before Run, SetProxy, or AddTLSConfig.
func (h *HTTPSender) SetHTTPClient(client *http.Client) {
	h.client = client
}

// SetMaxMessageSize sets the maximum message size in bytes. Messages
// larger than this limit are rejected before sending.
func (h *HTTPSender) SetMaxMessageSize(maxMessageSize int64) {
	h.maxMessageSize = internal.ResolveMaxMessageSize(maxMessageSize)
}

// SetProxy will force each request to use passed proxy and use the passed headers when making a CONNECT request to the proxy.
// If the proxy has no schema http is used.
// This method is not thread safe and must be called before h.client is used.
func (h *HTTPSender) SetProxy(proxy string, headers http.Header) error {
	proxyURL, err := url.Parse(proxy)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" { // error or bad URL - try to use http as scheme to resolve
		proxyURL, err = url.Parse("http://" + proxy)
		if err != nil {
			return err
		}
	}
	if proxyURL.Hostname() == "" {
		return url.InvalidHostError(proxy)
	}

	proxyTransport := &http.Transport{}
	if h.client.Transport != nil {
		transport, ok := h.client.Transport.(*http.Transport)
		if !ok {
			return fmt.Errorf("unable to coorce client transport as *http.Transport detected type is: %T", h.client.Transport)
		}
		proxyTransport = transport.Clone()
	}
	proxyTransport.Proxy = http.ProxyURL(proxyURL)
	proxyTransport.ProxyConnectHeader = headers
	h.client.Transport = proxyTransport
	return nil
}

// Run starts the processing loop that will perform the HTTP request/response.
// When there are no more messages to send Run will suspend until either there is
// a new message to send or the polling interval elapses.
// Should not be called concurrently with itself. Can be called concurrently with
// modifying NextMessage().
// Run continues until ctx is cancelled.
func (h *HTTPSender) Run(
	ctx context.Context,
	serverURL string,
	callbacks types.Callbacks,
	clientSyncedState *ClientSyncedState,
	packagesStateProvider types.PackagesStateProvider,
	packageSyncMutex *sync.Mutex,
	reporterInterval time.Duration,
	payloadVerifier signing.Verifier,
	tofuStore signing.TOFUStore,
) {
	h.url = serverURL
	h.callbacks = callbacks
	h.receiveProcessor = newReceivedProcessor(h.logger, callbacks, h, clientSyncedState, packagesStateProvider, packageSyncMutex, reporterInterval)
	if payloadVerifier != nil || tofuStore != nil {
		var serverName string
		if parsed, err := url.Parse(h.url); err == nil {
			serverName = parsed.Hostname()
		}
		h.attestation = newAttestationState(payloadVerifier, serverName, tofuStore)
	}

	// we need to detect if the redirect was ever set, if not, we want default behaviour
	if callbacks.CheckRedirect != nil {
		h.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			// viaResp only non-nil for ws client
			return callbacks.CheckRedirect(req, via, nil)
		}
	}

	// attestBackoff mirrors the pattern used by the WebSocket client's
	// runUntilStopped: attestation failures at the application level
	// are distinct from transport errors (the TCP connection is fine,
	// the server just failed verification). Without a separate backoff
	// the agent would retry at the full polling rate — up to 1 req/s
	// for aggressive heartbeat intervals — against a potentially
	// compromised server. Exponential backoff with no max elapsed time
	// matches the WS client's behaviour.
	attestBackoff := backoff.NewExponentialBackOff()
	attestBackoff.MaxElapsedTime = 0

	for {
		pollingTimer := time.NewTimer(time.Millisecond * time.Duration(h.pollingIntervalMs.Load()))
		select {
		case <-h.hasPendingMessage:
			// Have something to send. Stop the polling timer and send what we have.
			pollingTimer.Stop()
			if attestationFailed := h.makeOneRequestRoundtrip(ctx); attestationFailed {
				interval := attestBackoff.NextBackOff()
				h.logger.Errorf(ctx, "Payload trust verification failed, will retry in %v.", interval)
				timer := time.NewTimer(interval)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			} else {
				attestBackoff.Reset()
			}

		case <-pollingTimer.C:
			// Polling interval has passed. Force a status update.
			h.NextMessage().Update(func(msg *protobufs.AgentToServer) {})
			// This will make hasPendingMessage channel readable, so we will enter
			// the case above on the next iteration of the loop.
			h.ScheduleSend()

		case <-ctx.Done():
			return
		}
	}
}

// SetRequestHeader sets additional HTTP headers to send with all future requests.
// Should not be called concurrently with any other method.
func (h *HTTPSender) SetRequestHeader(baseHeaders http.Header, headerFunc func(http.Header) http.Header) {
	if baseHeaders == nil {
		baseHeaders = http.Header{}
	}

	if headerFunc == nil {
		headerFunc = func(h http.Header) http.Header {
			return h
		}
	}

	h.getHeader = func() http.Header {
		requestHeader := headerFunc(baseHeaders.Clone())
		requestHeader.Set(headerContentType, contentTypeProtobuf)
		if h.compressionEnabled {
			requestHeader.Set(headerContentEncoding, encodingTypeGZip)
		}

		return requestHeader
	}
}

// makeOneRequestRoundtrip sends a request and receives a response.
// It will retry the request if the server responds with too many
// requests or unavailable status. It returns true if the response
// failed attestation verification so the caller can apply backoff.
func (h *HTTPSender) makeOneRequestRoundtrip(ctx context.Context) bool {
	resp, err := h.sendRequestWithRetries(ctx)
	if err != nil {
		h.logger.Errorf(ctx, "%v", err)
		return false
	}
	if resp == nil {
		// No request was sent and nothing to receive.
		return false
	}
	return h.receiveResponse(ctx, resp)
}

// requestResult represents the outcome of a single HTTP request attempt.
type requestResult struct {
	resp     *http.Response
	err      error
	retry    bool
	interval time.Duration
}

func (h *HTTPSender) sendRequestWithRetries(ctx context.Context) (*http.Response, error) {
	req, err := h.prepareRequest(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Debugf(ctx, "Client is stopped, will not try anymore.")
		} else {
			h.logger.Errorf(ctx, "Failed prepare request (%v), will not try anymore.", err)
		}
		return nil, err
	}
	if req == nil {
		// Nothing to send.
		return nil, nil
	}

	// Repeatedly try requests with a backoff strategy.
	infiniteBackoff := backoff.NewExponentialBackOff()
	// Make backoff run forever.
	infiniteBackoff.MaxElapsedTime = 0

	interval := time.Duration(0)

	for {
		timer := time.NewTimer(interval)
		interval = infiniteBackoff.NextBackOff()

		select {
		case <-timer.C:
			result := h.attemptRequest(ctx, req, interval)

			if !result.retry {
				return result.resp, result.err
			}

			// Update interval if retry was requested with a specific interval.
			if result.interval > 0 {
				interval = result.interval
			}

			// Log and notify about the retryable failure.
			h.logger.Errorf(ctx, "Failed to do HTTP request (%v), will retry", result.err)
			h.callbacks.OnConnectFailed(ctx, result.err)

		case <-ctx.Done():
			h.logger.Debugf(ctx, "Client is stopped, will not try anymore.")
			return nil, ctx.Err()
		}
	}
}

// attemptRequest performs a single HTTP request attempt and returns a result indicating
// whether to retry or return.
func (h *HTTPSender) attemptRequest(ctx context.Context, req *requestWrapper, currentInterval time.Duration) requestResult {
	req.rewind(ctx)

	resp, err := h.client.Do(req.Request)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			h.logger.Debugf(ctx, "Client is stopped, will not try anymore.")
			return requestResult{resp: nil, err: err, retry: false}
		}
		// other errors are retryable.
		return requestResult{resp: nil, err: err, retry: true}
	}

	// Handle HTTP response status codes.
	switch resp.StatusCode {
	case http.StatusOK:
		h.callbacks.OnConnect(ctx)
		return requestResult{resp: resp, err: nil, retry: false}

	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		retryInterval := recalculateInterval(currentInterval, resp)
		if err := h.discardResponseBody(resp); err != nil {
			return requestResult{resp: nil, err: err, retry: false}
		}
		return requestResult{
			resp:     nil,
			err:      fmt.Errorf("server response code=%d", resp.StatusCode),
			retry:    true,
			interval: retryInterval,
		}

	default:
		if err := h.discardResponseBody(resp); err != nil {
			return requestResult{resp: nil, err: err, retry: false}
		}
		return requestResult{
			resp:  nil,
			err:   fmt.Errorf("invalid response from server: %d", resp.StatusCode),
			retry: false,
		}
	}
}

func recalculateInterval(interval time.Duration, resp *http.Response) time.Duration {
	retryAfter := internal.ExtractRetryAfterHeader(resp)
	if retryAfter.Defined && retryAfter.Duration > interval {
		// If the Server suggested connecting later than our interval
		// then honour Server's request, otherwise wait at least
		// as much as we calculated.
		interval = retryAfter.Duration
	}
	return interval
}

func (h *HTTPSender) prepareRequest(ctx context.Context) (*requestWrapper, error) {
	msgToSend := h.nextMessage.PopPending()
	if msgToSend == nil || proto.Equal(msgToSend, &protobufs.AgentToServer{}) {
		// There is no pending message or the message is empty.
		// Nothing to send.
		return nil, nil
	}

	data, err := proto.Marshal(msgToSend)
	if err != nil {
		return nil, err
	}

	if err := internal.CheckSizeLimit(int64(len(data)), h.maxMessageSize, "request body"); err != nil {
		return nil, err
	}

	r, err := http.NewRequestWithContext(ctx, OpAMPPlainHTTPMethod, h.url, nil)
	if err != nil {
		return nil, err
	}
	req := requestWrapper{Request: r}

	if h.compressionEnabled {
		var buf bytes.Buffer
		g := gzip.NewWriter(&buf)
		if _, err = g.Write(data); err != nil {
			h.logger.Errorf(ctx, "Failed to compress message: %v", err)
			return nil, err
		}
		if err = g.Close(); err != nil {
			h.logger.Errorf(ctx, "Failed to close the writer: %v", err)
			return nil, err
		}
		req.bodyReader = bodyReader(buf.Bytes())
	} else {
		req.bodyReader = bodyReader(data)
	}
	// Set GetBody so the standard library can replay the body when following
	// 307/308 redirects (which preserve the request method).
	r.GetBody = func() (io.ReadCloser, error) { return req.bodyReader(), nil }

	req.Header = h.getHeader()

	if msgToSend.InstanceUid != nil {
		uid, err := uuid.FromBytes(msgToSend.InstanceUid)
		if err != nil {
			return nil, err
		}
		req.Header.Set(headerOpAMPInstanceUID, uid.String())
	}

	return &req, nil
}

// receiveResponse decodes and processes a server response. It returns
// true when the response failed payload trust verification so the
// caller can apply attestation-specific backoff before retrying.
func (h *HTTPSender) receiveResponse(ctx context.Context, resp *http.Response) bool {
	msgBytes, err := h.readResponseBody(resp)
	if err != nil {
		h.logger.Errorf(ctx, "cannot read response body: %v", err)
		return false
	}

	var response protobufs.ServerToAgent
	if err := unwrapServerToAgent(ctx, h.attestation, msgBytes, &response); err != nil {
		// When payload trust verification is enabled, a failure here
		// means the response cannot be trusted; the spec says the
		// connection MUST be terminated. For HTTP polling the agent
		// has no persistent connection to drop, so we skip processing
		// this response and Reset the per-connection attestation
		// state. The next poll will re-attempt the trust-chain
		// handshake, allowing the Agent to recover from mid-stream
		// faults such as server-side key rotation. Without the Reset,
		// the cached firstSeen flag would keep us in the "verify
		// signature" branch and the Agent could be stuck rejecting
		// every subsequent response.
		//
		// Use the same sentinel string the WebSocket receive path
		// emits ("Payload trust verification failed") so operators
		// can grep for one canonical phrase across both transports.
		if h.attestation != nil && isAttestationFailure(err) {
			h.logger.Errorf(ctx, "Payload trust verification failed; resetting attestation state: %v", err)
			h.attestation.Reset()
			return true
		}
		h.logger.Errorf(ctx, "cannot unmarshal response: %v", err)
		return false
	}

	h.receiveProcessor.ProcessReceivedMessage(ctx, &response)
	return false
}

// readResponseBody reads the response body, decompressing gzip if indicated
// by Content-Encoding, and enforces maxMessageSize.
func (h *HTTPSender) readResponseBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	if resp.Header.Get(headerContentEncoding) == encodingTypeGZip {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		return internal.ReadAllLimited(gr, h.maxMessageSize, "response body")
	}
	return internal.ReadAllLimited(resp.Body, h.maxMessageSize, "response body")
}

// discardResponseBody drains and closes the response body, decompressing
// gzip if indicated by Content-Encoding and enforcing maxMessageSize. This
// allows the underlying TCP connection to be reused for subsequent requests.
func (h *HTTPSender) discardResponseBody(resp *http.Response) error {
	defer resp.Body.Close()
	if resp.Header.Get(headerContentEncoding) == encodingTypeGZip {
		gr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return err
		}
		defer gr.Close()
		return internal.CopyDiscardLimited(gr, h.maxMessageSize, "response body")
	}
	return internal.CopyDiscardLimited(resp.Body, h.maxMessageSize, "response body")
}

func (h *HTTPSender) SetHeartbeatInterval(duration time.Duration) error {
	if duration <= 0 {
		return errors.New("heartbeat interval for httpclient must be greater than zero")
	}

	if duration != 0 {
		h.SetPollingInterval(duration)
	}

	return nil
}

// SetPollingInterval sets the interval between polling. Has effect starting from the
// next polling cycle.
func (h *HTTPSender) SetPollingInterval(duration time.Duration) {
	h.pollingIntervalMs.Store(duration.Milliseconds())
}

// EnableCompression enables compression for the sender.
// Should not be called concurrently with Run.
func (h *HTTPSender) EnableCompression() {
	h.compressionEnabled = true
}

func (h *HTTPSender) AddTLSConfig(config *tls.Config) {
	if config != nil {
		tlsTransport := &http.Transport{}
		if h.client.Transport != nil {
			if transport, ok := h.client.Transport.(*http.Transport); ok {
				tlsTransport = transport.Clone()
			}
		}
		tlsTransport.TLSClientConfig = config
		h.client.Transport = tlsTransport
	}
}
