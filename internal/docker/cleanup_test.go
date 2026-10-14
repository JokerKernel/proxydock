package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupRecordTempsOnlyRemoveOwnedFiles(t *testing.T) {
	for _, operation := range []string{"recover", "cleanup"} {
		t.Run(operation, func(t *testing.T) {
			m, fake := deployed(t)
			ctx := context.Background()
			if operation == "cleanup" {
				if err := m.Remove(ctx); err != nil {
					t.Fatal(err)
				}
			}
			parent := filepath.Dir(m.paths.CleanupRecord())
			owned, err := jsonBytes(ownership{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance})
			if err != nil {
				t.Fatal(err)
			}
			remove := map[string][]byte{
				cleanupWritePrefix + strings.Repeat("a", 24): []byte(`{"schema_version":`),
				".docker-write-" + strings.Repeat("b", 24):   owned,
				".docker-write-123":                          owned,
			}
			keep := map[string][]byte{
				".docker-write-" + strings.Repeat("c", 24): []byte(`{"schema_version":`),
				".docker-write-" + strings.Repeat("d", 24): []byte(`{"schema_version":1,"managed_by":"proxyforge","instance_id":"other"}`),
				".docker-write-" + strings.Repeat("e", 24): append(append([]byte{}, owned...), owned...),
				".docker-write-notes":                      []byte("user notes"),
			}
			for name, data := range remove {
				if err := os.WriteFile(filepath.Join(parent, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for name, data := range keep {
				if err := os.WriteFile(filepath.Join(parent, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			public := filepath.Join(parent, cleanupWritePrefix+strings.Repeat("f", 24))
			if err := os.WriteFile(public, owned, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(public, 0644); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside.json")
			if err := os.WriteFile(outside, owned, 0600); err != nil {
				t.Fatal(err)
			}
			symlink := filepath.Join(parent, cleanupWritePrefix+strings.Repeat("1", 24))
			if err := os.Symlink(outside, symlink); err != nil {
				t.Fatal(err)
			}
			hardlink := filepath.Join(parent, cleanupWritePrefix+strings.Repeat("2", 24))
			if err := os.Link(outside, hardlink); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(parent, cleanupWritePrefix+strings.Repeat("3", 24))
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			unchanged := managedSnapshots(t, m)
			var output bytes.Buffer
			m.errOut = &output
			if operation == "cleanup" {
				err = m.Cleanup(ctx)
			} else {
				err = m.Recover(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			for name := range remove {
				if _, err := os.Lstat(filepath.Join(parent, name)); !os.IsNotExist(err) {
					t.Fatalf("owned temporary file remains: %s %v", name, err)
				}
			}
			for name, data := range keep {
				if !bytes.Equal(readFile(t, filepath.Join(parent, name)), data) {
					t.Fatalf("unknown temporary file changed: %s", name)
				}
				if name != ".docker-write-notes" && !strings.Contains(output.String(), filepath.Join(parent, name)) {
					t.Fatalf("retained legacy file not reported: %s\n%s", name, output.String())
				}
			}
			for _, path := range []string{public, symlink, hardlink, directory} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("unsafe entry deleted: %s %v", path, err)
				}
				if !strings.Contains(output.String(), path) {
					t.Fatalf("unsafe entry not reported: %s\n%s", path, output.String())
				}
			}
			if !bytes.Equal(readFile(t, outside), owned) {
				t.Fatal("outside file changed")
			}
			if operation == "recover" {
				for _, snap := range unchanged {
					if snap.Exists && !bytes.Equal(snap.Data, readFile(t, snap.Path)) {
						t.Fatalf("recovery changed active file: %s", snap.Path)
					}
				}
				if !fake.container.State.Running {
					t.Fatal("temporary recovery stopped container")
				}
			} else if m.hasData() || !strings.Contains(output.String(), "保留 7 个无法确认安全删除的残留") {
				t.Fatalf("cleanup result hid remaining files: %s", output.String())
			}
		})
	}
}

func TestCleanupRemovesEmptyParentsAndCanRepeat(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(m.paths.CleanupRecord())
	stale := filepath.Join(parent, cleanupWritePrefix+strings.Repeat("a", 24))
	if err := os.WriteFile(stale, nil, 0600); err != nil {
		t.Fatal(err)
	}
	images := len(fake.images)
	export := filepath.Join(t.TempDir(), "client.yaml")
	if err := m.WriteExport(export, []byte("user export"), false); err != nil {
		t.Fatal(err)
	}
	program := m.paths.resolve("/usr/local/sbin/proxydock")
	if err := atomicWrite(program, []byte("dummy program"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	m.errOut = &output
	for i := 0; i < 2; i++ {
		if err := m.Cleanup(ctx); err != nil {
			t.Fatalf("cleanup attempt %d: %v", i, err)
		}
		for _, path := range []string{m.paths.Deploy(), m.paths.Data(), m.paths.CleanupRecord(), filepath.Dir(m.paths.Deploy()), parent, m.paths.resolve("/var/lib/proxyforge")} {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("cleanup left empty directory or data: %s %v", path, err)
			}
		}
		if _, err := os.Stat(m.paths.Lock()); err != nil {
			t.Fatalf("instance lock deleted: %v", err)
		}
		if len(fake.images) != images || string(readFile(t, export)) != "user export" || string(readFile(t, program)) != "dummy program" {
			t.Fatal("cleanup deleted protected resources")
		}
	}
	for _, want := range []string{"回收空目录", "保留锁文件", "并发保护", "保留资源", "用户导出配置", "实例数据已清理"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("cleanup result missing %q: %s", want, output.String())
		}
	}
}

func TestCleanupPreservesSharedParentContents(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		m.paths.resolve("/opt/proxyforge/other/config.json"),
		m.paths.resolve("/var/lib/proxyforge/docker/other/config.json"),
		m.paths.resolve("/var/lib/proxyforge/state/xray.json"),
	}
	for _, path := range paths {
		if err := atomicWrite(path, []byte("other data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	m.errOut = &output
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if m.hasData() {
		t.Fatal("instance data remains")
	}
	for _, path := range paths {
		if string(readFile(t, path)) != "other data" {
			t.Fatalf("shared parent contents changed: %s", path)
		}
	}
	for _, parent := range []string{filepath.Dir(m.paths.Deploy()), filepath.Dir(m.paths.Data()), m.paths.resolve("/var/lib/proxyforge")} {
		if !strings.Contains(output.String(), parent+"（包含其他文件）") {
			t.Fatalf("retained parent not reported: %s", output.String())
		}
	}
}

func TestCleanupResumesInterruptedDeletionWithScopedTemporaryFile(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.saveCleanupRecord(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(m.paths.Deploy()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.paths.Owner()); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(m.paths.CleanupRecord()), cleanupWritePrefix+strings.Repeat("a", 24))
	if err := os.WriteFile(stale, []byte("partial record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(ctx); err == nil || !strings.Contains(err.Error(), "清理尚未完成") {
		t.Fatalf("recovery bypassed pending cleanup: %v", err)
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("recovery changed pending cleanup artifacts: %v", err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if m.hasData() {
		t.Fatal("resumed cleanup left data or its recovery record")
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("resumed cleanup left temporary record: %v", err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupRecordTempsWaitForSuccessfulTransactionRecovery(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	if _, err := m.begin("config", fake.container); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(filepath.Dir(m.paths.CleanupRecord()), cleanupWritePrefix+strings.Repeat("a", 24))
	if err := os.WriteFile(stale, []byte("partial record"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.unavailable = true
	if err := m.Recover(ctx); err == nil {
		t.Fatal("expected transaction recovery failure")
	}
	for _, path := range []string{stale, m.paths.Journal()} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("failed recovery discarded artifact: %s %v", path, err)
		}
	}
	fake.unavailable = false
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("successful recovery retained temporary record: %v", err)
	}
}

func TestEmptyDirectoryRemovalDoesNotFollowSymlinkReplacement(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.MkdirAll(filepath.Join(parent, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outside, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := openSecureDir(parent, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	moved := filepath.Join(base, "original")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if err := dir.removeDir("empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "empty")); err != nil {
		t.Fatal("removed directory through replacement symlink")
	}
	if err := os.Symlink(filepath.Join(outside, "empty"), filepath.Join(moved, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := dir.removeDir("alias"); err == nil {
		t.Fatal("directory removal accepted symlink")
	}
	if _, err := os.Lstat(filepath.Join(moved, "alias")); err != nil {
		t.Fatal("directory removal unlinked symlink")
	}
	if err := dir.removeDir("../outside"); err == nil {
		t.Fatal("directory removal accepted traversal")
	}
}
