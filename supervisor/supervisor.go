package supervisor

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/oklog/ulid/v2"
	"github.com/open-telemetry/opamp-go/client"
	"github.com/open-telemetry/opamp-go/supervisor/agent"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Logger struct {
	Logger *log.Logger
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.Logger.Printf(format, v...)
}

type supervisor struct {
	logger *Logger

	configFile string
	config     *Configuration
	tls        *tls.Config

	instanceID string

	client client.OpAMPClient

	agentSettings *agent.Settings
	agent         agent.Agent
	watcher       agent.Watcher
	configurer    agent.Configurer
	packager      agent.Packager
}

// New creates a new supervisor based on the provided configuration.
func New(configFile string, logger *Logger) *supervisor {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(0)), 0)
	return &supervisor{
		configFile: configFile,
		client:     client.New(logger),
		instanceID: ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String(),
	}
}

func (s *supervisor) createAgent() error {
	panic("implement me")
}

func (s *supervisor) createAndStartAgent(ctx context.Context) error {
	if err := s.createAgent(); err != nil {
		return fmt.Errorf("failed to create an agent from the specified configuration: %w", err)
	}

	if err := s.watcher.Watch(ctx); err != nil {
		return fmt.Errorf("failed to start the watchdog: %w", err)
	}

	return nil
}

func (s *supervisor) createAndStartClient() error {
	settings := client.StartSettings{
		OpAMPServerURL: s.config.OpAMPServer.Endpoint,
		TLSConfig:      s.tls,
		InstanceUid:    s.instanceID,
		Callbacks:      s,
	}

	if s.configurer != nil {
		effectiveConfig, err := s.configurer.LoadEffectiveConfig(s.agentSettings.ConfigSpec)
		if err != nil {
			return fmt.Errorf("failed to retrieve agent's effective config: %w", err)
		}
		settings.LastEffectiveConfig = effectiveConfig
	}

	return s.client.Start(settings)
}

func (s *supervisor) Start() error {
	// Load the supervisor configuration.
	config, err := s.loadConfig()
	if err != nil {
		return fmt.Errorf("failed to load supervisor configuration: %w", err)
	}
	s.config = config

	ctx := context.Background()

	if err := s.createAndStartAgent(ctx); err != nil {
		return fmt.Errorf("failed to start the agent: %w", err)
	}

	if err := s.createAndStartClient(); err != nil {
		return fmt.Errorf("failed to start the client: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	s.logger.Debugf("Received termination signal: %s", sig)

	if err := s.client.Stop(ctx); err != nil {
		s.logger.Debugf("Failed to stop the client: %v", err)
	}
	if err := s.watcher.Shutdown(ctx); err != nil {
		s.logger.Debugf("Failed to stop the watcher: %v", err)
	}

	return nil
}
