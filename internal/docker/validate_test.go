package docker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

type validationOutputRunner struct {
	*simulatedDocker
	output []byte
	err    error
}

func (r *validationOutputRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "docker" && len(args) > 2 && args[2] == "run" && contains(args, "-test") {
		r.calls = append(r.calls, append([]string(nil), args[2:]...))
		return r.output, r.err
	}
	return r.simulatedDocker.Run(ctx, name, args...)
}

func TestValidationReportsDockerOutputBeforeAnyLiveChange(t *testing.T) {
	for _, tc := range []struct{ name, exit, output string }{
		{"daemon-error", "exit status 125", "docker: Error response from daemon: invalid mount config for type bind\n"},
		{"xray-error", "exit status 23", "Xray\nFailed to start: failed to load config: invalid port range\n"},
		{"no-output", "exit status 125", " \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, fake := deployed(t)
			unchanged := savedOperationFiles(t, m)
			failure := errors.New(tc.exit)
			m.runner = &validationOutputRunner{simulatedDocker: fake, output: []byte(tc.output), err: failure}
			fake.calls = nil
			var log bytes.Buffer
			m.errOut = &log
			entry := accessEntry(t, "example.com")
			entry.Ports = model.DefaultAccessDomainPorts
			err := m.SetPolicy(context.Background(), model.AccessPolicy{Whitelist: []model.AccessEntry{entry}})
			if !errors.Is(err, failure) {
				t.Fatalf("validation did not preserve the command error: %v", err)
			}
			details := append([]string{tc.exit}, strings.Split(strings.TrimSpace(tc.output), "\n")...)
			for _, detail := range details {
				if !strings.Contains(err.Error(), detail) || !strings.Contains(log.String(), detail) {
					t.Fatalf("missing failure details %q: error=%v log=%s", detail, err, log.String())
				}
			}
			unchanged()
			if fake.container == nil || !fake.container.State.Running {
				t.Fatal("validation failure stopped the existing service")
			}
			for _, call := range fake.calls {
				if call[0] == "compose" && (contains(call, "up") || contains(call, "restart") || contains(call, "stop") || contains(call, "down")) {
					t.Fatalf("validation failure changed the service: %v", call)
				}
			}
		})
	}
}
