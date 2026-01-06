package config

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"time"

	"github.com/open-telemetry/opamp-go/internal/examples/certs"
	"go.opentelemetry.io/collector/config/configopaque"
	"go.opentelemetry.io/collector/config/configtls"
)

type AgentConfig struct {
	Endpoint          string                 `mapstructure:"endpoint"`
	HeartbeatInterval *time.Duration         `mapstructure:"heartbeat_interval"`
	TLSSetting        configtls.ClientConfig `mapstructure:"tls,omitempty"`
}

func (a *AgentConfig) GetTLSConfig(ctx context.Context) (*tls.Config, error) {
	parsedURL, err := url.Parse(a.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse server endpoint: %w", err)
	}

	if parsedURL.Scheme != "wss" && parsedURL.Scheme != "https" {
		return nil, nil
	}

	// Default settings
	if len(a.TLSSetting.CAFile) == 0 && len(a.TLSSetting.KeyFile) == 0 && len(a.TLSSetting.CertFile) == 0 {
		a.TLSSetting.CAPem = configopaque.String(certs.CaCert)
	}

	return a.TLSSetting.LoadTLSConfig(ctx)
}
