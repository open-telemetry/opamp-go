package supervisor

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/open-telemetry/opamp-go/internal"
	"github.com/open-telemetry/opamp-go/internal/examples/server/data"
	"github.com/open-telemetry/opamp-go/internal/examples/server/opampsrv"
)

func changeCurrentDir(t *testing.T) string {
	t.Helper()

	tmp := t.TempDir()

	oldCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}

	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("changing working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldCWD); err != nil {
			t.Fatalf("restoring working directory: %v", err)
		}
	})

	return tmp
}

func startOpampServer(t *testing.T) {
	t.Helper()

	opampSrv := opampsrv.NewServer(&data.AllAgents)
	opampSrv.Start()

	t.Cleanup(func() {
		opampSrv.Stop()
	})
}

func TestNewSupervisor(t *testing.T) {
	tmpDir := changeCurrentDir(t)
	os.WriteFile("supervisor.yaml", []byte(fmt.Sprintf(`
server:
  endpoint: ws://127.0.0.1:4320/v1/opamp
agent:
  executable: %s/dummy_agent.sh`, tmpDir)), 0644)

	os.WriteFile("dummy_agent.sh", []byte("#!/bin/sh\nsleep 9999\n"), 0755)

	startOpampServer(t)

	supervisor, err := NewSupervisor(&internal.NopLogger{})
	assert.NoError(t, err)

	supervisor.Shutdown()
}

func TestShutdownRaceCondition(t *testing.T) {
	tmpDir := changeCurrentDir(t)
	os.WriteFile("supervisor.yaml", []byte(fmt.Sprintf(`
server:
  endpoint: ws://127.0.0.1:4320/v1/opamp
agent:
  executable: %s/dummy_agent.sh`, tmpDir)), 0644)

	os.WriteFile("dummy_agent.sh", []byte("#!/bin/sh\nsleep 9999\n"), 0755)

	startOpampServer(t)

	// There's no great way to ensure Shutdown gets called before Start.
	// The DelayLogger ensures some delay before the goroutine gets started.
	var supervisor *Supervisor
	var err error
	supervisor, err = NewSupervisor(&internal.DelayLogger{})
	supervisor.Shutdown()
	supervisor.hasNewConfig <- struct{}{}

	assert.NoError(t, err)

	// The Shutdown method has been called before the runAgentProcess goroutine
	// gets started and has a chance to load a new process. Make sure no PID
	// has been launched.
	assert.Never(t, func() bool {
		return supervisor.commander.Pid() != 0
	}, 2*time.Second, 10*time.Millisecond)
}
