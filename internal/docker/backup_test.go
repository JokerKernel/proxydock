package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxydock/internal/docker/model"
)

func assertConfigOnlyBackup(t *testing.T, dir string, want []byte) {
	t.Helper()
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 || files[0].Name() != "config.json" || !files[0].Type().IsRegular() {
		t.Fatalf("backup must contain only config.json: %s %v %v", dir, files, err)
	}
	path := filepath.Join(dir, "config.json")
	if !bytes.Equal(readFile(t, path), want) {
		t.Fatalf("backup did not preserve raw config bytes: %s", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions: %v %v", info, err)
	}
}

func TestFirstDeploymentBacksUpActualConfigOnly(t *testing.T) {
	m, _ := prepared(t)
	if entries, err := os.ReadDir(m.paths.Backups()); err == nil && len(entries) != 0 {
		t.Fatal("prepare retained an empty backup")
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	backups, err := os.ReadDir(m.paths.Backups())
	if err != nil || len(backups) != 1 {
		t.Fatalf("initial backup count: %v %v", backups, err)
	}
	assertConfigOnlyBackup(t, filepath.Join(m.paths.Backups(), backups[0].Name()), readFile(t, m.paths.Config()))
	transactions, err := os.ReadDir(m.paths.Transactions())
	if err != nil || len(transactions) != 0 {
		t.Fatalf("completed transaction snapshots remain: %v %v", transactions, err)
	}
}

func TestConfigurationBackupPrecedesChangeAndRollbackRecordsAreTemporary(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	j, err := m.begin("config", f.container)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(j.Backup) != m.paths.Transactions() {
		t.Fatalf("rollback snapshot stored as permanent backup: %s", j.Backup)
	}
	assertConfigOnlyBackup(t, filepath.Join(m.paths.Backups(), filepath.Base(j.Backup)), before)
	if err := m.cleanupTransactionSnapshots(); err == nil {
		t.Fatal("active recovery data can be deleted")
	}
	if _, err := os.Stat(filepath.Join(j.Backup, "snapshot.json")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(m.paths.Config(), []byte(`{"invalid-for-managed-state":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, m.paths.Config()), before) || !f.container.State.Running {
		t.Fatal("recovery lost original config or running state")
	}
	if _, err := os.Stat(j.Backup); !os.IsNotExist(err) {
		t.Fatalf("finished rollback left temporary snapshot: %v", err)
	}
}

func TestFailedRollbackKeepsTemporarySnapshotUntilRecovery(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	f.healthFailures = 2
	if err := m.PatchSetting(context.Background(), "log", "debug"); err == nil || !strings.Contains(err.Error(), "回滚未完成") {
		t.Fatalf("expected incomplete rollback: %v", err)
	}
	var j journal
	if err := loadJSON(m.paths.Journal(), &j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.Backup, "snapshot.json")); err != nil {
		t.Fatal("incomplete recovery lost its snapshot")
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, m.paths.Config()), before) {
		t.Fatal("retry did not restore config")
	}
	if _, err := os.Stat(j.Backup); !os.IsNotExist(err) {
		t.Fatalf("recovery did not clear temporary files: %v", err)
	}
}

func TestBackupMaintenanceOnlyReadsConfigurationFiles(t *testing.T) {
	m, _ := deployed(t)
	name := m.now().UTC().Add(time.Second).Format("20060102T150405.000000000Z") + "-aaaaaaaaaaaa"
	unsupported := filepath.Join(m.paths.Backups(), name, "snapshot.json")
	if err := atomicWrite(unsupported, []byte("unsupported format"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pruneBackups(); err != nil {
		t.Fatalf("maintenance tried parsing another backup format: %v", err)
	}
	if !bytes.Equal(readFile(t, unsupported), []byte("unsupported format")) {
		t.Fatal("maintenance converted another backup format")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(unsupported), "config.json")); !os.IsNotExist(err) {
		t.Fatalf("maintenance created a converted configuration: %v", err)
	}
}

func TestTransactionRecoveryRequiresCurrentSnapshotDirectory(t *testing.T) {
	m, f := deployed(t)
	before := readFile(t, m.paths.Config())
	j, err := m.begin("config", f.container)
	if err != nil {
		t.Fatal(err)
	}
	j.Backup = filepath.Join(m.paths.Backups(), filepath.Base(j.Backup))
	if err := m.restoreTransaction(context.Background(), j); err == nil {
		t.Fatal("recovery accepted snapshots from the configuration backup directory")
	}
	if !bytes.Equal(before, readFile(t, m.paths.Config())) || !f.container.State.Running {
		t.Fatal("unsupported recovery changed the deployment")
	}
}
