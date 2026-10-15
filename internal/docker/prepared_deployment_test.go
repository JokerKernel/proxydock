package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func requirePreparedWithoutConfig(t *testing.T, m *Manager, f *simulatedDocker) {
	t.Helper()
	md, err := m.metadata()
	if err != nil || md.ComposeSHA256 != "" || md.DesiredState != "stopped" || f.container != nil {
		t.Fatalf("invalid prepared deployment: %+v %v", md, err)
	}
	for _, path := range []string{m.paths.Compose(), m.paths.Config(), m.paths.State(), m.paths.Journal(), m.paths.Backups()} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("prepared deployment contains configuration data: %s %v", path, err)
		}
	}
	if status, err := m.Status(context.Background()); err != nil || status.Label != "待配置" {
		t.Fatalf("prepared deployment status: %+v %v", status, err)
	}
}

func TestPreparedParametersApplyToFirstConfiguration(t *testing.T) {
	m, f := prepared(t)
	ctx := context.Background()
	for _, action := range []func() error{
		func() error { return m.SetRunAsRoot(ctx, true) },
		func() error { return m.SetRunAsRoot(ctx, true) },
		func() error { return m.SetNetwork(ctx, networkPublish) },
		func() error { return m.SetNetwork(ctx, networkPublish) },
		func() error { return m.Upgrade(ctx, "26.3.27") },
		func() error {
			// Before any configuration exists, updating needs no rollback image.
			f.images = map[string]Metadata{}
			return m.Upgrade(ctx, "26.4.1")
		},
		func() error { return m.Upgrade(ctx, "latest") },
	} {
		if err := action(); err != nil {
			t.Fatal(err)
		}
		requirePreparedWithoutConfig(t, m, f)
	}
	for _, call := range f.calls {
		if call[0] == "compose" && !contains(call, "version") {
			t.Fatalf("prepared parameter update invoked Compose: %v", call)
		}
	}
	md, err := m.metadata()
	if err != nil || !md.RunAsRoot || md.Network != networkPublish || md.Channel != "latest" || md.RequestedVersion != "26.4.1" {
		t.Fatalf("prepared parameter updates were lost: %+v %v", md, err)
	}
	f.calls = nil
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	compose := readFile(t, m.paths.Compose())
	for _, part := range []string{md.ImageRef, `user: "0:0"`, `"8443:8443"`, "read_only: true", "NET_BIND_SERVICE", "json-file"} {
		if !bytes.Contains(compose, []byte(part)) {
			t.Fatalf("first Compose missing %q:\n%s", part, compose)
		}
	}
	if f.container == nil || !f.container.State.Running || f.container.Image != md.ImageID || f.container.Config.User != "0:0" {
		t.Fatal("first configuration did not start the selected image and user")
	}
	md, err = m.metadata()
	if err != nil || md.ComposeSHA256 != digest(compose) {
		t.Fatalf("first Compose digest not recorded: %+v %v", md, err)
	}
	if count := countDockerCalls(f, func(c []string) bool { return c[0] == "compose" && contains(c, "config") }); count != 1 {
		t.Fatalf("first Compose was validated %d times", count)
	}
}

func TestFirstConfigurationFailureReturnsToPreparedState(t *testing.T) {
	for _, failure := range []string{"compose", "state", "startup", "health"} {
		t.Run(failure, func(t *testing.T) {
			m, f := prepared(t)
			before := readFile(t, m.paths.Metadata())
			switch failure {
			case "compose":
				f.failComposeConfig = 1
			case "state":
				m.stateWrite = func(string, []byte, os.FileMode) error { return errors.New("state write failed") }
			case "startup":
				f.failComposeUp = 1
			case "health":
				f.healthFailures = 1
			}
			opts := model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}
			if err := m.Generate(context.Background(), opts, true); err == nil || !strings.Contains(err.Error(), "已恢复") {
				t.Fatalf("expected restored first config failure: %v", err)
			}
			requirePreparedWithoutConfig(t, m, f)
			if !bytes.Equal(before, readFile(t, m.paths.Metadata())) {
				t.Fatal("first config failure changed prepared parameters")
			}
			m.stateWrite = atomicWrite
			if err := m.Generate(context.Background(), opts, true); err != nil {
				t.Fatalf("first config retry failed: %v", err)
			}
		})
	}
}

func TestPreparedServiceControlsRequireConfiguration(t *testing.T) {
	m, f := prepared(t)
	f.calls = nil
	for _, action := range []string{"start", "restart"} {
		if _, err := m.Service(context.Background(), action); err == nil || !strings.Contains(err.Error(), "请先生成配置") {
			t.Fatalf("%s bypassed first configuration: %v", action, err)
		}
	}
	if _, err := m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	requireNoRuntimeChanges(t, f)
	requirePreparedWithoutConfig(t, m, f)
}

func TestPreparedStateDoesNotAllowUnknownOrMissingCompose(t *testing.T) {
	for _, kind := range []string{"unexpected-config", "unexpected-container", "missing-compose"} {
		t.Run(kind, func(t *testing.T) {
			m, f := prepared(t)
			switch kind {
			case "unexpected-config":
				if err := atomicWrite(m.paths.Config(), []byte(`{"foreign":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unexpected-container":
				md, err := m.metadata()
				if err != nil {
					t.Fatal(err)
				}
				f.newContainer(md.ImageID)
			case "missing-compose":
				if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(m.paths.Compose()); err != nil {
					t.Fatal(err)
				}
			}
			before := readFile(t, m.paths.Metadata())
			f.calls = nil
			ctx := context.Background()
			for _, action := range []func() error{
				func() error { _, err := m.Status(ctx); return err },
				func() error {
					return m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true)
				},
				func() error { return m.Upgrade(ctx, "26.4.1") },
				func() error { return m.SetRunAsRoot(ctx, true) },
				func() error { return m.SetNetwork(ctx, networkPublish) },
				func() error { _, err := m.Service(ctx, "start"); return err },
			} {
				if err := action(); err == nil {
					t.Fatal("operation accepted an inconsistent prepared deployment")
				}
			}
			requireNoRuntimeChanges(t, f)
			if !bytes.Equal(before, readFile(t, m.paths.Metadata())) {
				t.Fatal("rejected operations changed deployment parameters")
			}
		})
	}
}
