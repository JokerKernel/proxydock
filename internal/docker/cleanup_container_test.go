package docker

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

type cleanupRunner struct {
	*auditRuntimeRunner
	stopErr       error
	keepRunning   bool
	removeErr     error
	keepContainer bool
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
		if a[1] == "rm" {
			readFile(r.t, r.manager.paths.Config())
			if r.container == nil || r.container.State.Running || r.container.State.Restarting {
				r.t.Fatal("cleanup removed a container before confirming it stopped")
			}
			if r.removeErr != nil || r.keepContainer {
				r.calls = append(r.calls, append([]string(nil), a...))
				return nil, r.removeErr
			}
		}
	}
	return r.auditRuntimeRunner.Run(ctx, name, args...)
}

func TestCleanupRemovesContainerAndAllowsReinstallation(t *testing.T) {
	for _, state := range []string{"running", "stopped", "restarting"} {
		t.Run(state, func(t *testing.T) {
			m, f := deployed(t)
			f.container.State.Running = state == "running"
			f.container.State.Restarting = state == "restarting"
			original := f.container
			r := &cleanupRunner{auditRuntimeRunner: &auditRuntimeRunner{simulatedDocker: f, bridge: true}}
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
			if f.container != nil || r.bridge {
				t.Fatal("cleanup did not remove the container and its unused network")
			}
			if m.hasData() || string(readFile(t, external)) != "export" || len(f.images) == 0 {
				t.Fatal("cleanup did not limit deletion to managed instance data")
			}
			stops, removals := 0, 0
			for _, call := range f.calls {
				if call[0] == "container" && call[1] == "stop" {
					stops++
					if call[2] != original.ID {
						t.Fatalf("stopped another container: %v", call)
					}
				}
				if call[0] == "container" && call[1] == "rm" {
					removals++
					if len(call) != 3 || call[2] != original.ID {
						t.Fatalf("cleanup removed another container or used force: %v", call)
					}
				}
				if call[0] == "image" && call[1] == "rm" || call[0] == "compose" && (contains(call, "down") || contains(call, "up")) {
					t.Fatalf("cleanup removed images or recreated the container: %v", call)
				}
			}
			wantStops := 1
			if state == "stopped" {
				wantStops = 0
			}
			if stops != wantStops {
				t.Fatalf("got %d stops, want %d", stops, wantStops)
			}
			if removals != 1 {
				t.Fatalf("cleanup removed %d containers", removals)
			}
			if err := m.prepareWithNetwork(context.Background(), "latest", networkPublish, false); err != nil {
				t.Fatalf("reinstallation after cleanup failed: %v", err)
			}
			if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 57807}, true); err != nil {
				t.Fatalf("configuration after reinstallation failed: %v", err)
			}
			if f.container == nil || !f.container.State.Running {
				t.Fatal("reinstallation did not start a new instance")
			}
		})
	}
}

func TestCleanupRetainsDataWhenContainerRemovalFails(t *testing.T) {
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove-error", true: "container-remains"}[silent], func(t *testing.T) {
			m, f := deployed(t)
			unchanged := savedOperationFiles(t, m)
			r := &cleanupRunner{auditRuntimeRunner: &auditRuntimeRunner{simulatedDocker: f, bridge: true}, keepContainer: silent}
			if !silent {
				r.removeErr = errors.New("container removal failed")
			}
			m.runner = r
			if err := m.Cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "未删除实例数据") {
				t.Fatalf("expected container removal failure: %v", err)
			}
			unchanged()
			if f.container == nil || f.container.State.Running || !r.bridge {
				t.Fatal("failed removal did not retain the stopped container and network")
			}
			r.removeErr, r.keepContainer = nil, false
			if err := m.Cleanup(context.Background()); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			if f.container != nil || r.bridge || m.hasData() {
				t.Fatal("retry left container or instance data")
			}
		})
	}
}

func TestCleanupRemovesOwnedContainerWithoutInstanceData(t *testing.T) {
	m, f := deployed(t)
	ctx := context.Background()
	if _, err := m.Service(ctx, "stop"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{m.paths.Deploy(), m.paths.Data()} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if f.container != nil || m.hasData() {
		t.Fatal("cleanup left an owned container without data")
	}
	if err := m.Prepare(ctx, "latest"); err != nil {
		t.Fatalf("retained container still blocks installation: %v", err)
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
