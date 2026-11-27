package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"proxydock/internal/docker/model"
)

func inactiveDeployment(t *testing.T, removed bool) (*Manager, *simulatedDocker) {
	t.Helper()
	m, f := fallbackDeployment(t, networkPublish, true)
	if removed {
		if err := m.Remove(context.Background()); err != nil {
			t.Fatal(err)
		}
	} else if _, err := m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	return m, f
}

func TestConfigurationChangesStartStoppedOrRemovedContainer(t *testing.T) {
	ctx := context.Background()
	changes := map[string]func(*testing.T, *Manager) error{
		"setting": func(t *testing.T, m *Manager) error { return m.PatchSetting(ctx, "log", "debug") },
		"generate": func(t *testing.T, m *Manager) error {
			return m.Generate(ctx, model.GenerateOptions{Port: 9443}, false)
		},
		"edit": func(t *testing.T, m *Manager) error {
			root := configRoot(t, readFile(t, m.paths.Config()))
			root["custom"] = map[string]any{"preserved": true}
			return m.ApplyEdited(ctx, rootBytes(t, root))
		},
		"reset": func(t *testing.T, m *Manager) error { return m.Reset(ctx, model.ResetOptions{Credentials: true}) },
		"policy": func(t *testing.T, m *Manager) error {
			entry, err := model.ParseAccessEntry("example.org", false)
			if err != nil {
				return err
			}
			return m.SetPolicy(ctx, model.AccessPolicy{Whitelist: []model.AccessEntry{entry}})
		},
		"landing": func(t *testing.T, m *Manager) error {
			_, err := m.AddLanding(ctx, "edge", "tls", 9443)
			return err
		},
	}
	for _, removed := range []bool{false, true} {
		for name, change := range changes {
			t.Run(map[bool]string{false: "stopped/", true: "removed/"}[removed]+name, func(t *testing.T) {
				m, f := inactiveDeployment(t, removed)
				old, err := m.metadata()
				if err != nil {
					t.Fatal(err)
				}
				if err := change(t, m); err != nil {
					t.Fatal(err)
				}
				md, err := m.metadata()
				if err != nil || md.DesiredState != "running" || md.ImageID != old.ImageID {
					t.Fatalf("configuration did not activate the saved deployment: %+v %v", md, err)
				}
				if f.container == nil || !f.container.State.Running || f.container.Image != md.ImageID {
					t.Fatal("configuration did not start the container")
				}
				if _, _, err := m.managedConfig(); err != nil {
					t.Fatal(err)
				}
				if _, err := m.inspect(ctx); err != nil {
					t.Fatal(err)
				}
				if count := countDockerCalls(f, func(c []string) bool { return c[0] == "compose" && contains(c, "up") && contains(c, "-d") }); count != 1 {
					t.Fatalf("configuration started the container %d times", count)
				}
				for _, call := range f.calls {
					if call[0] == "image" && call[1] == "pull" || contains(call, "--no-start") {
						t.Fatalf("configuration pulled a new image or left the container stopped: %v", call)
					}
				}
			})
		}
	}
}

func TestConfigurationActivationFailureRestoresInactiveDeployment(t *testing.T) {
	for _, removed := range []bool{false, true} {
		for _, failure := range []string{"validation", "port-busy", "startup", "health", "state-write"} {
			t.Run(map[bool]string{false: "stopped/", true: "removed/"}[removed]+failure, func(t *testing.T) {
				m, f := inactiveDeployment(t, removed)
				before := managedSnapshots(t, m)
				portsChecked := 0
				m.portFree = func(int) error {
					portsChecked++
					if failure == "port-busy" {
						return errors.New("port is busy")
					}
					return nil
				}
				switch failure {
				case "validation":
					f.rejectValidation = true
				case "startup":
					f.failComposeUp = 1
				case "health":
					f.healthFailures = 1
				case "state-write":
					m.stateWrite = func(string, []byte, os.FileMode) error { return errors.New("disk write failed") }
				}
				if err := m.PatchSetting(context.Background(), "log", "debug"); err == nil {
					t.Fatal("expected configuration activation failure")
				}
				if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
					t.Fatal("failed activation changed configuration or deployment parameters")
				}
				if removed && f.container != nil || !removed && (f.container == nil || f.container.State.Running) {
					t.Fatal("failed activation did not restore the previous container state")
				}
				if failure != "validation" && portsChecked == 0 {
					t.Fatal("starting an inactive instance skipped the existing port check")
				}
				if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
					t.Fatalf("rollback left an unfinished transaction: %v", err)
				}
				f.rejectValidation = false
				m.portFree = func(int) error { return nil }
				m.stateWrite = atomicWrite
				if err := m.PatchSetting(context.Background(), "log", "debug"); err != nil {
					t.Fatalf("retry failed: %v", err)
				}
				if f.container == nil || !f.container.State.Running || bytes.Equal(before[1].Data, readFile(t, m.paths.Config())) {
					t.Fatal("retry did not activate the new configuration")
				}
			})
		}
	}
}
