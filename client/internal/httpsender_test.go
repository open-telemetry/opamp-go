package internal

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

type serverResponse struct {
	statusCode int
	body       []byte
	headers    map[string]string
}

func TestHTTPSenderRunsUntilContextCancelled(t *testing.T) {

	srv := StartMockServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	url := "http://" + srv.Endpoint
	callbacks := types.CallbacksStruct{
		OnConnectFunc: func() {
			cancel()
		},
	}
	capabilities := protobufs.AgentCapabilities_AcceptsPackages | protobufs.AgentCapabilities_ReportsPackageStatuses
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.SetPollingInterval(time.Millisecond * 10)
	sender.Run(
		ctx, url, callbacks, &ClientSyncedState{}, NewInMemPackagesStore(), capabilities,
	)
}

func TestHTTPSenderRespectsRetryAfterHeader(t *testing.T) {

	type retryTestCase struct {
		tries    []serverResponse
		duration time.Duration
	}

	testCases := []retryTestCase{
		{
			tries: []serverResponse{
				{statusCode: 429, headers: map[string]string{"Retry-After": "5"}},
				{statusCode: 200},
			},
			duration: time.Duration(5 * time.Second),
		},
		{
			tries: []serverResponse{
				{statusCode: 503, headers: map[string]string{"Retry-After": "3"}},
				{statusCode: 200},
			},
			duration: time.Duration(2 * time.Second),
		},
		{
			tries: []serverResponse{
				{statusCode: 503, headers: map[string]string{"Retry-After": "2"}},
				{statusCode: 503, headers: map[string]string{"Retry-After": "3"}},
				{statusCode: 503, headers: map[string]string{"Retry-After": "5"}},
				{statusCode: 200},
			},
			duration: time.Duration(10 * time.Second),
		},
	}

	for _, testCase := range testCases {
		var connectionAttempts int64
		srv := StartMockServer(t)
		srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
			attempt := int(atomic.LoadInt64(&connectionAttempts))
			atomic.AddInt64(&connectionAttempts, 1)
			headers := testCase.tries[attempt].headers
			code := testCase.tries[attempt].statusCode
			body := testCase.tries[attempt].body
			fmt.Println(headers, code, body)
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(code)
			w.Write(body)
		}
		ctx, cancel := context.WithCancel(context.Background())
		url := "http://" + srv.Endpoint
		sender := NewHTTPSender(&sharedinternal.NopLogger{})
		sender.SetPollingInterval(time.Millisecond * 10)
		sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
			msg.AgentDescription = &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{{
					Key: "service.name",
					Value: &protobufs.AnyValue{
						Value: &protobufs.AnyValue_StringValue{StringValue: "test-service"},
					},
				}},
			}
		})
		sender.callbacks = types.CallbacksStruct{
			OnConnectFunc: func() {
			},
			OnConnectFailedFunc: func(_ error) {
			},
		}
		sender.url = url
		start := time.Now()
		resp, err := sender.sendRequestWithRetries(ctx)
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		fmt.Println(time.Since(start), testCase.duration)
		assert.True(t, time.Since(start) > testCase.duration)
		cancel()
	}
}

func TestHTTPSenderSendsMessageToReceiveProcessorOnSuccess(t *testing.T) {
	srv := StartMockServer(t)
	msg := &protobufs.ServerToAgent{
		InstanceUid: "id",
		PackagesAvailable: &protobufs.PackagesAvailable{
			Packages: map[string]*protobufs.PackageAvailable{
				"first_package": {
					Type:    protobufs.PackageAvailable_AddonPackage,
					Version: "0.1",
					File: &protobufs.DownloadableFile{
						DownloadUrl: "url",
						ContentHash: []byte{0, 6, 9},
					},
					Hash: []byte{1, 4, 2, 0},
				},
			},
			AllPackagesHash: []byte{1},
		},
	}
	srv.OnRequest = func(w http.ResponseWriter, r *http.Request) {
		data, _ := proto.Marshal(msg)
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}
	ctx, cancel := context.WithCancel(context.Background())
	url := "http://" + srv.Endpoint
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.SetPollingInterval(time.Millisecond * 10)
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.AgentDescription = &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "string"},
				},
			}},
		}
	})
	var msgReceived int64
	sender.callbacks = types.CallbacksStruct{
		OnConnectFunc: func() {
		},
		OnMessageFunc: func(ctx context.Context, received *types.MessageData) {
			exp, _ := proto.Marshal(msg.PackagesAvailable)
			rec, _ := proto.Marshal(received.PackagesAvailable)
			assert.Equal(t, exp, rec)
			atomic.StoreInt64(&msgReceived, 1)
		},
	}

	sender.url = url
	capabilities := protobufs.AgentCapabilities_AcceptsPackages | protobufs.AgentCapabilities_ReportsPackageStatuses
	sender.receiveProcessor = newReceivedProcessor(sender.logger, sender.callbacks, sender, nil, nil, capabilities)
	sender.makeOneRequestRoundtrip(ctx)
	assert.Equal(t, atomic.LoadInt64(&msgReceived), int64(1))
	cancel()
}

func TestHTTPSenderReturnsWithoutRetryOnCancelledContext(t *testing.T) {

	srv := StartMockServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	url := "http://" + srv.Endpoint
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.SetPollingInterval(time.Millisecond * 10)
	sender.NextMessage().Update(func(msg *protobufs.AgentToServer) {
		msg.AgentDescription = &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key: "service.name",
				Value: &protobufs.AnyValue{
					Value: &protobufs.AnyValue_StringValue{StringValue: "string"},
				},
			}},
		}
	})
	sender.url = url
	cancel()
	_, err := sender.sendRequestWithRetries(ctx)
	assert.Error(t, err)
}

func TestHTTPSenderReturnsNothingToSend(t *testing.T) {

	srv := StartMockServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	url := "http://" + srv.Endpoint
	sender := NewHTTPSender(&sharedinternal.NopLogger{})
	sender.url = url
	resp, err := sender.sendRequestWithRetries(ctx)
	assert.Nil(t, resp)
	assert.Nil(t, err)
	cancel()
}
