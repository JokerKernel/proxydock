package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type cleanupRunner struct {
	*auditRuntimeRunner
	stopErr     error
	keepRunning bool
}

func (r *cleanupRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	a := args[2:]
	if len(a) > 1 && a[0] == "container" {
		if a[1] == "ls" && contains(a, "label=io.proxydock.instance="+Instance) && !contains(a, "--no-trunc") && r.container != nil {
			return []byte(r.container.ID[:12] + "\n"), nil
		}
		if a[1] == "stop" {
			// Files must remain available until the container has stopped.
			readFile(r.t, r.manager.paths.Config())
			if r.stopErr != nil || r.keepRunning {
				r.calls = append(r.calls, append([]string(nil), a...))
				return nil, r.stopErr
			}
		}
	}
	return r.auditRuntimeRunner.Run(ctx, name, args...)
}

func TestCleanupStopsAndRetainsContainer(t *testing.T) {
	for _, state := range []string{"running", "stopped", "restarting"} {
		t.Run(state, func(t *testing.T) {
			m, f := deployed(t)
			f.container.State.Running = state == "running"
			f.container.State.Restarting = state == "restarting"
			original := f.container
			r := &cleanupRunner{auditRuntimeRunner: &auditRuntimeRunner{simulatedDocker: f, bridge: true, attached: true}}
			m.runner = r
			f.calls = nil
			external := m.paths.resolve("/var/lib/proxydock/export.json")
			if err := os.WriteFile(external, []byte("export"), 0600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := m.Cleanup(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if f.container != original || f.container.State.Running || f.container.State.Restarting || !r.bridge {
				t.Fatal("cleanup did not retain the stopped container and its network")
			}
			if m.hasData() || string(readFile(t, external)) != "export" || len(f.images) == 0 {
				t.Fatal("cleanup did not limit deletion to managed instance data")
			}
			stops := 0
			for _, call := range f.calls {
				if call[0] == "container" && call[1] == "stop" {
					stops++
					if call[2] != original.ID {
						t.Fatalf("stopped another container: %v", call)
					}
				}
				if call[0] == "network" || call[0] == "container" && call[1] == "rm" || call[0] == "compose" && (contains(call, "down") || contains(call, "up")) {
					t.Fatalf("cleanup removed or recreated runtime resources: %v", call)
				}
			}
			wantStops := 1
			if state == "stopped" {
				wantStops = 0
			}
			if stops != wantStops {
				t.Fatalf("got %d stops, want %d", stops, wantStops)
			}
		})
	}
}

func TestCleanupRetainsDataWhenStopFails(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop-error", true: "still-running"}[silent], func(t *testing.T) {
			m, f := deployed(t)
			unchanged := savedOperationFiles(t, m)
			r := &cleanupRunner{auditRuntimeRunner: &auditRuntimeRunner{simulatedDocker: f, bridge: true}, keepRunning: silent}
			if !silent {
				r.stopErr = errors.New("stop failed")
			}
			m.runner = r
			f.calls = nil
			if err := m.Cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "未删除实例数据") {
				t.Fatalf("expected stop failure: %v", err)
			}
			unchanged()
			if !f.container.State.Running || !r.bridge {
				t.Fatal("failed cleanup changed runtime resources")
			}
			if _, err := os.Stat(m.paths.CleanupRecord()); !os.IsNotExist(err) {
				t.Fatalf("failed stop started data deletion: %v", err)
			}
		})
	}
}
