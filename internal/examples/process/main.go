/*
 * Copyright (c) AppDynamics, Inc., and its affiliates 2020
 * All Rights Reserved.
 * THIS IS UNPUBLISHED PROPRIETARY CODE OF APPDYNAMICS, INC.
 *
 * The copyright notice above does not evidence any actual or
 * intended publication of such source code
 */

package process

import (
	"errors"
	"log"
	"os/exec"
	"strings"

	"github.com/shirou/gopsutil/process"
)

var (
	binary = "/Users/phdevava/Documents/Git/opentelemetry-collector/bin/otelcorecol_darwin_amd64"
	args   = []string{"--config", "/Users/phdevava/Documents/Git/opentelemetry-collector/examples/local/otel-config.yaml"}

	errNotFound = errors.New("otel collector process not found")
)

func RestartOtelCol() error {
	pr, err := getOtelColProcess()
	if err != nil && errors.Is(err, errNotFound) {
		return err
	}
	if err = killProcess(pr); err != nil {
		return err
	}
	go startOtelCol()
	return nil
}

func getOtelColProcess() (*process.Process, error) {
	processes, err := process.Processes()
	if err != nil {
		return nil, err
	}
	for _, p := range processes {
		s, err := p.Cmdline()
		if err != nil {
			return nil, err
		}
		if strings.Contains(s, "otel") {
			return p, nil
		}
	}
	return nil, errNotFound
}

func killProcess(ps *process.Process) error {
	for i := 0; i < 3; i++ {
		if err := ps.Terminate(); err != nil {
			i++
			continue
		} else {
			return nil
		}
	}
	if err := ps.Kill(); err != nil {
		return err
	}
	return nil
}

func startOtelCol() {
	cmd := exec.Command(binary, args...)
	if err := cmd.Run(); err != nil {
		log.Panic(err)
	}
}
