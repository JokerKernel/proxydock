package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestMaintenanceCleanupFailureCanRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires normal user for permission simulation")
	}
	m, _ := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(m.paths.Config())
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if err := m.Cleanup(ctx); err == nil {
		t.Fatal("cleanup should fail to remove read-only config directory")
	} else {
		t.Logf("first cleanup: %v", err)
	}
	if _, err := os.Stat(m.paths.Owner()); err != nil {
		t.Fatalf("failed deployment cleanup lost owner: %v", err)
	}
	if _, err := os.Stat(m.paths.Config()); err != nil {
		t.Fatalf("deployment unexpectedly removed: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	if m.hasData() {
		t.Fatal("cleanup left data or cleanup record")
	}
}

func TestMaintenanceFailedOperationsLimitBackups(t *testing.T) {
	m, f := deployed(t)
	for i := 0; i < 6; i++ {
		f.failRestart = 1
		if err := m.PatchSetting(context.Background(), "log", "debug"); err == nil {
			t.Fatal("expected restart failure")
		}
	}
	entries, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("backup count not limited: %d", len(entries))
	}
	t.Logf("confirmed: %d backup directories after successful bounded rollbacks", len(entries))
}

func TestMaintenanceSnapshotFailureRemovesIncompleteBackup(t *testing.T) {
	m, f := deployed(t)
	before, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(m.paths.Config(), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(m.paths.Config(), 0600)
	if _, err = m.begin("config", f.container); err == nil {
		t.Fatal("snapshot failure expected")
	}
	after, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("snapshot failure left backup: %d -> %d", len(before), len(after))
	}
	if _, err = os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatal("unexpected journal")
	}
	t.Log("failed snapshot left no directory or journal")
}

func TestMaintenanceOldCertificatesFollowBackupReferences(t *testing.T) {
	m, _ := prepared(t)
	ctx := context.Background()
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := m.Reset(ctx, model.ResetOptions{RotateCertificate: true}); err != nil {
			t.Fatal(err)
		}
	}
	certDirs, err := os.ReadDir(m.paths.TLS())
	if err != nil {
		t.Fatal(err)
	}
	backups, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 3 || len(certDirs) != 4 {
		t.Fatalf("unexpected counts: certs=%d backups=%d", len(certDirs), len(backups))
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{filepath.Dir(state.Node.TLSCertificate.CertificateFile): true}
	for _, backup := range backups {
		var snaps []fileSnapshot
		if err = loadSnapshot(filepath.Join(m.paths.Backups(), backup.Name(), "snapshot.json"), &snaps); err != nil {
			t.Fatal(err)
		}
		for _, snap := range snaps {
			for _, certDir := range certDirs {
				full := filepath.Join(m.paths.TLS(), certDir.Name())
				if strings.Contains(string(snap.Data), full) || strings.Contains(string(snap.Data), "/etc/xray/tls/"+certDir.Name()+"/") {
					refs[full] = true
				}
			}
		}
	}
	t.Logf("confirmed: %d certificate directories; only %d referenced by current state or retained backups", len(certDirs), len(refs))
	if len(refs) != len(certDirs) {
		t.Fatal("unreferenced certificates remain")
	}
}

func TestMaintenanceReclaimsFailedCertificateAfterRecovery(t *testing.T) {
	m, f := prepared(t)
	ctx := context.Background()
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(m.paths.TLS())
	if err != nil {
		t.Fatal(err)
	}
	f.healthFailures = 2
	if err = m.Reset(ctx, model.ResetOptions{RotateCertificate: true}); err == nil || !strings.Contains(err.Error(), "回滚未完成") {
		t.Fatalf("expected incomplete rollback, got %v", err)
	}
	if err = m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(m.paths.TLS())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("unused failed certificate retained: %d -> %d", len(before), len(after))
	}
	t.Log("unused new certificate reclaimed after eventual recovery")
}

func TestMaintenanceReclaimsStaleTemporaryFiles(t *testing.T) {
	m, _ := deployed(t)
	paths := []string{filepath.Join(m.paths.Staging(), "candidate-123456.json"), filepath.Join(m.paths.Staging(), "edit-654321.json"), filepath.Join(filepath.Dir(m.paths.Config()), ".docker-write-"+strings.Repeat("a", 24))}
	for _, path := range paths {
		if err := atomicWrite(path, []byte("stale secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale temporary file remains: %s %v", path, err)
		}
	}
	t.Log("simulated crash remnants reclaimed by recovery")
}

type auditRuntimeRunner struct {
	*simulatedDocker
	bridge           bool
	validator        bool
	foreignNetwork   bool
	attached         bool
	foreignValidator bool
}

func (r *auditRuntimeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	a := args[2:]
	record := func() { r.calls = append(r.calls, append([]string(nil), a...)) }
	validatorID := strings.Repeat("d", 64)
	networkID := strings.Repeat("e", 64)
	if len(a) > 1 && a[0] == "container" {
		if a[1] == "ls" && contains(a, "label=io.proxydock.instance="+Instance) && r.validator {
			record()
			return []byte(validatorID + "\n"), nil
		}
		if a[1] == "inspect" && a[2] == validatorID && r.validator {
			record()
			in := inspection{ID: validatorID, Name: "/" + Container + "-check-" + strings.Repeat("a", 24)}
			in.Config.Labels = map[string]string{"io.proxydock.managed": "true", "io.proxydock.instance": Instance, "io.proxydock.role": "validator"}
			if r.foreignValidator {
				in.Name = "/unrelated-container"
			}
			b, _ := json.Marshal([]inspection{in})
			return b, nil
		}
		if a[1] == "rm" && contains(a, validatorID) {
			record()
			r.validator = false
			return nil, nil
		}
	}
	if len(a) > 1 && a[0] == "network" {
		record()
		switch a[1] {
		case "ls":
			if r.bridge {
				return []byte(networkID + "\n"), nil
			}
			return nil, nil
		case "inspect":
			labels := map[string]string{"com.docker.compose.project": Project, "com.docker.compose.network": "default"}
			if r.foreignNetwork {
				labels["com.docker.compose.project"] = "foreign"
			}
			containers := map[string]any{}
			if r.attached {
				containers["other-container"] = map[string]any{"Name": "foreign"}
			}
			b, _ := json.Marshal([]map[string]any{{"Id": networkID, "Name": Project + "_default", "Labels": labels, "Containers": containers}})
			return b, nil
		case "rm":
			if r.attached {
				return nil, os.ErrPermission
			}
			r.bridge = false
			return nil, nil
		}
	}
	if len(a) > 0 && a[0] == "compose" {
		if contains(a, "up") {
			b, err := os.ReadFile(r.manager.paths.Compose())
			if err != nil {
				return nil, err
			}
			mode, _, err := parseComposeNetwork(b)
			if err != nil {
				return nil, err
			}
			if mode == networkPublish {
				r.bridge = true
			}
		}
		if contains(a, "down") && !r.attached {
			r.bridge = false
		}
	}
	return r.simulatedDocker.Run(ctx, name, args...)
}
func TestMaintenanceRollbackAndRemoveReclaimComposeNetwork(t *testing.T) {
	m, f := testManager(t)
	r := &auditRuntimeRunner{simulatedDocker: f}
	m.runner = r
	ctx := context.Background()
	if err := m.prepareWithNetwork(ctx, "26.3.27", networkPublish, false); err != nil {
		t.Fatal(err)
	}
	f.healthFailures = 1
	if err := m.Generate(ctx, model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true}, true); err == nil {
		t.Fatal("health failure expected")
	}
	if f.container != nil || r.bridge {
		t.Fatal("rollback left container or network")
	}
	r.bridge = true // Simulate an orphan network from an older program version.
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if r.bridge {
		t.Fatal("orphan bridge remains")
	}
	t.Log("rollback, remove and cleanup reclaim only the owned empty network")
}
func TestMaintenanceRecoveryReclaimsOrphanValidator(t *testing.T) {
	m, f := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	r := &auditRuntimeRunner{simulatedDocker: f, validator: true}
	m.runner = r
	if err := m.CheckCleanup(ctx); err == nil {
		t.Fatal("orphan validator should block cleanup")
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckCleanup(ctx); err != nil {
		t.Fatalf("validator remains after recovery: %v", err)
	}
	t.Log("recovery removed the orphan validator without removing business data")
}

func TestMaintenanceDeployRequiresConfirmationBeforeNetworkChange(t *testing.T) {
	m, f := deployed(t)
	cmd := m.Command()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"deploy", "--network", "publish"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "非交互") {
		t.Fatalf("expected confirmation error: %v", err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Network != networkHost || f.container.HostConfig.NetworkMode != networkHost {
		t.Fatal("unconfirmed deploy changed network")
	}
	t.Log("unconfirmed deploy leaves network and runtime unchanged")
}

func TestMaintenanceResumesCleanupWithoutOwner(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(m.paths.CleanupRecord(), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption while deleting Data: the independent record survives.
	if err := os.RemoveAll(m.paths.Deploy()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.paths.Owner()); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if m.hasData() {
		t.Fatal("cleanup left data or its recovery record")
	}
}

func TestMaintenanceCleanupRecordBlocksWritesAndRejectsForeignRecord(t *testing.T) {
	m, _ := deployed(t)
	before := managedSnapshots(t, m)
	if err := saveJSON(m.paths.CleanupRecord(), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(context.Background())
	if err != nil || status.Label != "清理未完成" {
		t.Fatalf("partial cleanup status: %+v, %v", status, err)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Port: 9443}, false); err == nil {
		t.Fatal("write proceeded during partial cleanup")
	}
	if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
		t.Fatal("write changed partial cleanup data")
	}
	if err := saveJSON(m.paths.CleanupRecord(), ownership{SchemaVersion: 1, ManagedBy: "foreign", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(context.Background()); err == nil {
		t.Fatal("foreign cleanup record accepted")
	}
	if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
		t.Fatal("foreign cleanup record caused deletion")
	}
}

func TestMaintenancePreservesPendingTransactionArtifacts(t *testing.T) {
	m, f := deployed(t)
	if _, err := m.begin("config", f.container); err != nil {
		t.Fatal(err)
	}
	journal := readFile(t, m.paths.Journal())
	cert, err := m.newCertificate("pending")
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(m.paths.Staging(), "candidate-998877.json")
	if err = atomicWrite(tmp, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	backups, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	m.maintainFiles()
	if _, err = os.Stat(tmp); err != nil {
		t.Fatal("pending candidate deleted")
	}
	if _, err = os.Stat(cert.KeyFile); err != nil {
		t.Fatal("pending certificate deleted")
	}
	after, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(backups) || !bytes.Equal(journal, readFile(t, m.paths.Journal())) {
		t.Fatal("pending backup or journal changed")
	}
}

func TestMaintenancePreservesManualAndImportedCertificateReferences(t *testing.T) {
	m, _ := deployed(t)
	live, err := m.newCertificate("manual-live")
	if err != nil {
		t.Fatal(err)
	}
	imported, err := m.newCertificate("manual-backup")
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := m.newCertificate("unused")
	if err != nil {
		t.Fatal(err)
	}
	root := configRoot(t, readFile(t, m.paths.Config()))
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{
		"tag": "custom", "listen": "127.0.0.1", "port": 10443, "protocol": "http",
		"streamSettings": map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"certificates": []any{map[string]any{"certificateFile": "/etc/xray/./tls/manual-live/cert.pem", "keyFile": "/etc/xray/tls/manual-live/key.pem"}}}},
	})
	data, err := jsonBytes(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Config(), data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	s.Node.ConfigSHA256 = digest(data)
	if err = m.saveNode(s.Node); err != nil {
		t.Fatal(err)
	}
	snaps := managedSnapshots(t, m)
	s.Node.TLSCertificate = &imported
	stateBytes, err := jsonBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	for i := range snaps {
		if snaps[i].Path == m.paths.State() {
			snaps[i].Data = stateBytes
		}
	}
	if err = saveJSON(filepath.Join(m.paths.Backups(), "imported", "snapshot.json"), snaps); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(m.paths.TLS(), "unmanaged", "notes.txt")
	if err = atomicWrite(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{live.KeyFile, imported.KeyFile, unknown} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("referenced or unknown artifact removed: %s", path)
		}
	}
	if _, err = os.Stat(orphan.KeyFile); !os.IsNotExist(err) {
		t.Fatalf("unused certificate remains: %v", err)
	}
}

func TestMaintenanceReclaimsInterruptedCertificateCreation(t *testing.T) {
	m, _ := deployed(t)
	dir := filepath.Join(m.paths.TLS(), "incomplete")
	if err := saveJSON(filepath.Join(dir, ".proxydock-certificate.json"), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(filepath.Join(dir, ".docker-write-"+strings.Repeat("a", 24)), []byte("partial key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("interrupted certificate directory remains: %v", err)
	}
}

func TestMaintenanceInvalidBackupStopsCertificateCollection(t *testing.T) {
	m, _ := deployed(t)
	cert, err := m.newCertificate("keep-on-error")
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(filepath.Join(m.paths.Backups(), "imported", "snapshot.json"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(cert.KeyFile); err != nil {
		t.Fatal("certificate deleted despite unreadable backup")
	}
}

func TestMaintenanceRefusesForeignOrUsedDockerResources(t *testing.T) {
	for _, resource := range []string{"foreign-network", "used-network", "foreign-validator"} {
		t.Run(resource, func(t *testing.T) {
			m, f := deployed(t)
			if err := m.Remove(context.Background()); err != nil {
				t.Fatal(err)
			}
			r := &auditRuntimeRunner{simulatedDocker: f}
			m.runner = r
			switch resource {
			case "foreign-network":
				r.bridge, r.foreignNetwork = true, true
			case "used-network":
				r.bridge, r.attached = true, true
			case "foreign-validator":
				r.validator, r.foreignValidator = true, true
			}
			before := managedSnapshots(t, m)
			if err := m.Remove(context.Background()); err == nil {
				t.Fatal("unsafe resource deletion accepted")
			}
			if !r.bridge && !r.validator {
				t.Fatal("unsafe resource deleted")
			}
			if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
				t.Fatal("unsafe resource changed managed data")
			}
		})
	}
}

func TestMaintenanceCleanupReclaimsOrphanResources(t *testing.T) {
	m, f := deployed(t)
	ctx := context.Background()
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	r := &auditRuntimeRunner{simulatedDocker: f, bridge: true, validator: true}
	m.runner = r
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if r.bridge || r.validator || m.hasData() {
		t.Fatal("cleanup left owned resources")
	}
}
