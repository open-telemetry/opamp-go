package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

const (
	defaultShutdownTimeout = 5 * time.Second
)

// NewWebSocket creates a new OpAMP Client that uses WebSocket transport.
func NewWebSocket(logger types.Logger) *wsClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	sender := internal.NewSender(logger)
	w := &wsClient{
		common:              internal.NewClientCommon(logger, sender),
		sender:              sender,
		connShutdownTimeout: defaultShutdownTimeout,
	}
	return w
}

func (c *wsClient) Stop(ctx context.Context) error {
	// AgentDisconnect MUST be set in the last AgentToServer message sent from the Client to the Server.
	c.sender.NextMessage().Update(
		func(msg *protobufs.AgentToServer) {
			msg.AgentDisconnect = &protobufs.AgentDisconnect{}
		},
	)
	c.sender.ScheduleSend()
	return c.common.Stop(ctx)
}

func (c *wsClient) AgentDescription() *protobufs.AgentDescription {
	return c.common.AgentDescription()
}

func (c *wsClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	return c.common.SetAgentDescription(descr)
}

func (c *wsClient) RequestConnectionSettings(request *protobufs.ConnectionSettingsRequest) error {
	return c.common.RequestConnectionSettings(request)
}

func (c *wsClient) SetHealth(health *protobufs.ComponentHealth) error {
	return c.common.SetHealth(health)
}

func (c *wsClient) UpdateEffectiveConfig(ctx context.Context) error {
	return c.common.UpdateEffectiveConfig(ctx)
}

func (c *wsClient) SetRemoteConfigStatus(status *protobufs.RemoteConfigStatus) error {
	return c.common.SetRemoteConfigStatus(status)
}

// SetConnectionSettingsStatus sets the current ConnectionSettingsStatus and sends
// it to the Server. Must be called after processing connection settings offers to
// report APPLIED or FAILED status.
func (c *wsClient) SetConnectionSettingsStatus(status *protobufs.ConnectionSettingsStatus) error {
	return c.common.SetConnectionSettingsStatus(status)
}

func (c *wsClient) SetPackageStatuses(statuses *protobufs.PackageStatuses) error {
	return c.common.SetPackageStatuses(statuses)
}

func (c *wsClient) SetCustomCapabilities(customCapabilities *protobufs.CustomCapabilities) error {
	return c.common.SetCustomCapabilities(customCapabilities)
}

func (c *wsClient) SetFlags(flags protobufs.AgentToServerFlags) {
	c.common.SetFlags(flags)
}

func (c *wsClient) SendCustomMessage(message *protobufs.CustomMessage) (messageSendingChannel chan struct{}, err error) {
	return c.common.SendCustomMessage(message)
}

// SetAvailableComponents implements OpAMPClient.SetAvailableComponents
func (c *wsClient) SetAvailableComponents(components *protobufs.AvailableComponents) error {
	return c.common.SetAvailableComponents(components)
}

// SetCapabilities implements OpAMPClient.
func (c *wsClient) SetCapabilities(capabilities *protobufs.AgentCapabilities) error {
	return c.common.SetCapabilities(capabilities)
}

func viaReq(resps []*http.Response) []*http.Request {
	reqs := make([]*http.Request, 0, len(resps))
	for _, resp := range resps {
		reqs = append(reqs, resp.Request)
	}
	return reqs
}

// handleRedirect checks a failed websocket upgrade response for a 3xx response
// and a Location header. If found, it sets the URL to the location found in the
// header so that it is tried on the next retry, instead of the current URL.
func (c *wsClient) handleRedirect(ctx context.Context, resp *http.Response) error {
	// append to the responseChain so that subsequent redirects will have access
	c.responseChain = append(c.responseChain, resp)

	// very liberal handling of 3xx that largely ignores HTTP semantics
	redirect, err := resp.Location()
	if err != nil {
		c.common.Logger.Errorf(ctx, "%d redirect, but no valid location: %s", resp.StatusCode, err)
		return err
	}

	// It's slightly tricky to make CheckRedirect work. The WS HTTP request is
	// formed within the websocket library. To work around that, copy the
	// previous request, available in the response, and set the URL to the new
	// location. It should then result in the same URL that the websocket
	// library will form.
	nextRequest := resp.Request.Clone(ctx)
	nextRequest.URL = redirect

	// if CheckRedirect results in an error, it gets returned, terminating
	// redirection. As with stdlib, the error is wrapped in url.Error.
	if c.common.Callbacks.CheckRedirect != nil {
		if err := c.common.Callbacks.CheckRedirect(nextRequest, viaReq(c.responseChain), c.responseChain); err != nil {
			return &url.Error{
				Op:  "Get",
				URL: nextRequest.URL.String(),
				Err: err,
			}
		}
	}

	// rewrite the scheme for the sake of tolerance
	if redirect.Scheme == "http" {
		redirect.Scheme = "ws"
	} else if redirect.Scheme == "https" {
		redirect.Scheme = "wss"
	}
	c.common.Logger.Debugf(ctx, "%d redirect to %s", resp.StatusCode, redirect)

	// Set the URL to the redirect, so that it connects to it on the
	// next cycle.
	c.url = redirect

	return nil
}

// Continuously try until connected. Will return nil when successfully
// connected. Will return error if it is cancelled via context.
func (c *wsClient) ensureConnected(ctx context.Context) error {
	infiniteBackoff := backoff.NewExponentialBackOff()

	// Make ticker run forever.
	infiniteBackoff.MaxElapsedTime = 0

	interval := time.Duration(0)

	for {
		timer := time.NewTimer(interval)
		interval = infiniteBackoff.NextBackOff()

		select {
		case <-timer.C:
			{
				if retryAfter, err := c.tryConnectOnce(ctx); err != nil {
					errCopy := err
					c.lastInternalErr.Store(&errCopy)
					if errors.Is(err, context.Canceled) {
						c.common.Logger.Debugf(ctx, "Client is stopped, will not try anymore.")
						return err
					} else {
						c.common.Logger.Errorf(ctx, "Connection failed (%v), will retry.", err)
					}
					// Retry again a bit later.

					if retryAfter.Defined && retryAfter.Duration > interval {
						// If the Server suggested connecting later than our interval
						// then honour Server's request, otherwise wait at least
						// as much as we calculated.
						interval = retryAfter.Duration
					}

					continue
				}
				// Connected successfully.
				return nil
			}

		case <-ctx.Done():
			c.common.Logger.Debugf(ctx, "Client is stopped, will not try anymore.")
			timer.Stop()
			return ctx.Err()
		}
	}
}

func (c *wsClient) runUntilStopped(ctx context.Context) {
	// Iterates until we detect that the client is stopping.
	sendFirstMessage := true
	for {
		if c.common.IsStopping() {
			return
		}

		c.runOneCycle(ctx, sendFirstMessage)
		sendFirstMessage = false
	}
}

// closeResponseChain closes the response bodies stored in the redirect chain
// and resets the slice. HTTP redirect responses must be closed to release the
// underlying connection back to the transport.
func (c *wsClient) closeResponseChain() {
	for _, r := range c.responseChain {
		if r.Body != nil {
			_ = r.Body.Close()
		}
	}
	c.responseChain = nil
}
