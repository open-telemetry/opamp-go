package supervisor

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/knadh/koanf"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/open-telemetry/opamp-go/supervisor/agent"
)

// TLSConfig describes basic client-side certificates to be used during
// client-side TLS authentication.
// TODO: these are just to fill in the gaps; not a lot of thought put into it.
type TLSConfig struct {
	// CertFile is the path to the client-side PEM-encoded file.
	CertFile string
	// KeyFile is the path to the key-file corresponding to the PEM file.
	KeyFile string
	// See tls.Config#InsecureSkipVerify.
	InsecureSkipVerify bool
}

func (t *TLSConfig) loadTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load %q with %q", t.CertFile, t.KeyFile)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: t.InsecureSkipVerify,
	}, nil
}

// OpAMPServer describes how to connect to an OpAMP server.
type OpAMPServer struct {
	Endpoint  string     `kaonf:"endpoint"`
	TLSConfig *TLSConfig `koanf:"tls_config"`
}

// Configuration describes this supervisor's configuration along with the
// Agent that it supervises.
type Configuration struct {
	OpAMPServer *OpAMPServer `koanf:"server"`

	AgentSettings *agent.Settings `koanf:"agent"`

	// WatchConfig describes the configuration that is used to monitor the
	// health of the agent.
	WatchConfig *agent.WatchConfig `kaonf:"watchdog"`
}

// Load the supervisor configuration.
func (s *supervisor) loadConfig() (*Configuration, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider(s.configFile), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("failed to read supervisor configuration: %w", err)
	}

	var config Configuration
	if err := k.Unmarshal("", &config); err != nil {
		return nil, fmt.Errorf("failed to parse supervisor configuration: %w", err)
	}

	if s.config.OpAMPServer == nil {
		return nil, errors.New("'server' section missing")
	}

	if s.config.AgentSettings == nil {
		return nil, errors.New("'agent' section missing")
	}

	if s.config.OpAMPServer.TLSConfig != nil {
		tc, err := s.config.OpAMPServer.TLSConfig.loadTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS configuration: %w", err)
		}
		s.tls = tc
	}

	return &config, nil
}
