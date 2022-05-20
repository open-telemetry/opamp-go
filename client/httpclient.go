package client

import (
	"context"

	"github.com/open-telemetry/opamp-go/client/internal"
	"github.com/open-telemetry/opamp-go/client/types"
	sharedinternal "github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// httpClient is an OpAMP Client implementation for plain HTTP transport.
// See specification: https://github.com/open-telemetry/opamp-spec/blob/main/specification.md#plain-http-transport
type httpClient struct {
	common internal.ClientCommon

	opAMPServerURL string

	// The sender performs HTTP request/response loop.
	sender *internal.HTTPSender
}

func NewHTTP(logger types.Logger) *httpClient {
	if logger == nil {
		logger = &sharedinternal.NopLogger{}
	}

	sender := internal.NewHTTPSender(logger)
	w := &httpClient{
		common: internal.NewClientCommon(logger, sender),
		sender: sender,
	}
	return w
}

func (c *httpClient) Start(ctx context.Context, settings types.StartSettings) error {
	if err := c.common.PrepareStart(ctx, settings); err != nil {
		return err
	}

	c.opAMPServerURL = settings.OpAMPServerURL

	// Prepare Server connection settings.
	c.sender.SetRequestHeader(settings.Header)

	// Prepare the first message to send.
	err := c.common.PrepareFirstMessage(ctx)
	if err != nil {
		return err
	}

	c.sender.ScheduleSend()

	c.common.StartConnectAndRun(c.runUntilStopped)

	return nil
}

func (c *httpClient) Stop(ctx context.Context) error {
	return c.common.Stop(ctx)
}

func (c *httpClient) AgentDescription() *protobufs.AgentDescription {
	return c.common.AgentDescription()
}

func (c *httpClient) SetAgentDescription(descr *protobufs.AgentDescription) error {
	return c.common.SetAgentDescription(descr)
}

func (c *httpClient) UpdateEffectiveConfig(ctx context.Context) error {
	return c.common.UpdateEffectiveConfig(ctx)
}

func (c *httpClient) runUntilStopped(ctx context.Context) {
	// Start the HTTP sender. This will make request/responses with retries for
	// failures and will wait with configured polling interval if there is nothing
	// to send.
	c.sender.Run(ctx, c.opAMPServerURL, c.common.Callbacks, &c.common.ClientSyncedState)
}
