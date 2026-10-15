package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configurationArtifacts(t *testing.T, m *Manager) func() {
	t.Helper()
	files := map[string][]byte{}
	for _, path := range []string{m.paths.Config(), m.paths.Compose(), m.paths.State()} {
		files[path] = readFile(t, path)
	}
	backups, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	for _, backup := range backups {
		path := filepath.Join(m.paths.Backups(), backup.Name(), "config.json")
		files[path] = readFile(t, path)
	}
	return func() {
		t.Helper()
		for path, want := range files {
			if !bytes.Equal(readFile(t, path), want) {
				t.Fatalf("operation changed configuration artifact: %s", path)
			}
		}
		after, err := os.ReadDir(m.paths.Backups())
		if err != nil || len(after) != len(backups) {
			t.Fatalf("operation rotated configuration backups: %v %v", after, err)
		}
		for i := range backups {
			if after[i].Name() != backups[i].Name() {
				t.Fatal("operation replaced a config backup")
			}
		}
		if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
			t.Fatalf("operation left a transaction: %v", err)
		}
		transactions, err := os.ReadDir(m.paths.Transactions())
		if err != nil || len(transactions) != 0 {
			t.Fatalf("operation left rollback snapshots: %v %v", transactions, err)
		}
	}
}

func TestServiceControlsDoNotSnapshotOrRotateConfig(t *testing.T) {
	m, _ := deployed(t)
	unchanged := configurationArtifacts(t, m)
	for _, action := range []string{"stop", "start", "restart", "logs"} {
		if _, err := m.Service(context.Background(), action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		unchanged()
	}
}

func TestServiceFailureDoesNotRestoreFilesOrRecreateContainer(t *testing.T) {
	for _, failure := range []string{"command", "health"} {
		t.Run(failure, func(t *testing.T) {
			m, f := deployed(t)
			unchanged := configurationArtifacts(t, m)
			if failure == "command" {
				f.failRestart = 1
			} else {
				f.healthFailures = 1
			}
			f.calls = nil
			if _, err := m.Service(context.Background(), "restart"); err == nil || strings.Contains(err.Error(), "回滚") {
				t.Fatalf("service failure should only report its error: %v", err)
			}
			for _, call := range f.calls {
				if call[0] == "container" && (call[1] == "stop" || call[1] == "rm") || call[0] == "compose" && contains(call, "up") {
					t.Fatalf("service failure recreated the container: %v", call)
				}
			}
			unchanged()
		})
	}
}

func TestChannelOnlyUpdateDoesNotCreateConfigurationTransaction(t *testing.T) {
	m, f := deployed(t)
	unchanged := configurationArtifacts(t, m)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	f.latestVersion, f.latestImageID = md.RequestedVersion, md.ImageID
	f.calls = nil
	if err := m.Upgrade(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	updated, err := m.metadata()
	if err != nil || updated.Channel != "latest" {
		t.Fatalf("channel update failed: %+v %v", updated, err)
	}
	requireNoRuntimeChanges(t, f)
	unchanged()
}

func TestReadOnlyOperationsDoNotCreateConfigurationTransaction(t *testing.T) {
	m, _ := deployed(t)
	unchanged := configurationArtifacts(t, m)
	ctx := context.Background()
	if _, err := m.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Client(ctx, "native", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ServerConfig(); err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged()
}

func TestDeploymentChangesDoNotCreateOrRotateConfigurationBackups(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	backups, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	want := readFile(t, filepath.Join(m.paths.Backups(), backups[0].Name(), "config.json"))
	operations := []func() error{
		func() error { return m.Upgrade(ctx, "latest") },
		func() error { return m.SetRunAsRoot(ctx, true) },
		func() error { return m.SetNetwork(ctx, networkPublish) },
	}
	for _, operation := range operations {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadDir(m.paths.Backups())
		if err != nil || len(after) != len(backups) || after[0].Name() != backups[0].Name() {
			t.Fatalf("deployment change rotated configuration backups: %v %v", after, err)
		}
		assertConfigOnlyBackup(t, filepath.Join(m.paths.Backups(), after[0].Name()), want)
	}
}
