package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"proxydock/internal/docker/model"
)

func TestPrivatePathsRejectSymlinkDotDotTraversal(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "child"), filepath.Join(base, "redirect")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	original := []byte("outside dummy data")
	if err := os.WriteFile(secret, original, 0600); err != nil {
		t.Fatal(err)
	}
	// filepath.Join would erase the component that caused the original bypass.
	raw := base + "/redirect/../secret"
	m, _ := testManager(t)
	checks := map[string]func() error{
		"check":  func() error { return safePath(raw) },
		"read":   func() error { _, err := readPrivate(raw); return err },
		"write":  func() error { return atomicWrite(raw, []byte("changed"), 0600) },
		"remove": func() error { return removePrivateFile(raw) },
		"export": func() error { return m.WriteExport(raw, []byte("changed"), true) },
		"create": func() error { return m.WriteExport(base+"/redirect/../new", []byte("changed"), false) },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("noncanonical path accepted")
			}
			if !bytes.Equal(readFile(t, secret), original) {
				t.Fatal("outside file changed")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatalf("outside export created: %v", err)
	}
}

func TestSecureDirectoryPinsOperationsAcrossSymlinkReplacement(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(base, "exports")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := openSecureDir(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	moved := filepath.Join(base, "original")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "client.json"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := dir.createTemp(".docker-export-")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("verified directory")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dir.rename(filepath.Base(f.Name()), "client.json"); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(moved, "client.json"))); got != "verified directory" {
		t.Fatal(got)
	}
	if err := dir.unlink("client.json"); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(outside, "client.json"))); got != "outside" {
		t.Fatalf("symlink target modified: %s", got)
	}
	if reopened, err := openSecureDir(path, false); err == nil {
		reopened.Close()
		t.Fatal("symlink directory accepted")
	}
}

func TestMaintenancePreservesFilesWithSimilarTemporaryPrefixes(t *testing.T) {
	m, _ := deployed(t)
	keep := []string{"candidate-notes.txt", "candidate-123.txt", "candidate-name.json", "edit-backup.json", ".docker-write-notes", ".edit-notes.json.swp"}
	remove := []string{"candidate-123.json", "edit-456.json", ".edit-456.json.swp", ".docker-write-123", ".docker-write-" + strings.Repeat("a", 24)}
	for _, name := range append(append([]string{}, keep...), remove...) {
		if err := os.WriteFile(filepath.Join(m.paths.Staging(), name), []byte("dummy"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A matching name is insufficient if the entry is not a private regular file.
	public := filepath.Join(m.paths.Staging(), "candidate-789.json")
	if err := os.WriteFile(public, []byte("public"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(m.paths.Staging(), "candidate-999.json")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range keep {
		if got := string(readFile(t, filepath.Join(m.paths.Staging(), name))); got != "dummy" {
			t.Fatalf("unrelated file changed: %s", name)
		}
	}
	for _, name := range remove {
		if _, err := os.Stat(filepath.Join(m.paths.Staging(), name)); !os.IsNotExist(err) {
			t.Fatalf("generated temporary file remains: %s %v", name, err)
		}
	}
	if _, err := os.Lstat(symlink); err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, outside)) != "outside" || string(readFile(t, public)) != "public" {
		t.Fatal("nonprivate or symlink target changed")
	}
}

func TestExpiredMainCertificateCanRotate(t *testing.T) {
	m, _ := prepared(t)
	ctx := context.Background()
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	before, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	now := m.now()
	m.now = func() time.Time { return now.AddDate(11, 0, 0) }
	if _, err := m.Client(ctx, "clash", ""); err == nil {
		t.Fatal("expired certificate should fail client validation")
	}
	if err := m.Reset(ctx, model.ResetOptions{RotateCertificate: true}); err != nil {
		t.Fatal(err)
	}
	after, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.UUID != before.Node.UUID || after.Node.TLSCertificate.CertificateFile == before.Node.TLSCertificate.CertificateFile {
		t.Fatal("certificate rotation changed credentials or retained old certificate")
	}
	if _, err := m.Client(ctx, "clash", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(before.Node.TLSCertificate.KeyFile); err != nil {
		t.Fatalf("backup certificate lost: %v", err)
	}
}

func TestExpiredCertificateRotationFailureRestoresOriginalFiles(t *testing.T) {
	m, fake := prepared(t)
	ctx := context.Background()
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	before := managedSnapshots(t, m)
	now := m.now()
	m.now = func() time.Time { return now.AddDate(11, 0, 0) }
	fake.failRestart = 1
	if err := m.Reset(ctx, model.ResetOptions{RotateCertificate: true}); err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatalf("rotation failure did not roll back: %v", err)
	}
	for _, snap := range before {
		if snap.Exists {
			if !bytes.Equal(snap.Data, readFile(t, snap.Path)) {
				t.Fatalf("rollback changed file: %s", snap.Path)
			}
		} else if _, err := os.Lstat(snap.Path); !os.IsNotExist(err) {
			t.Fatalf("rollback left new file: %s %v", snap.Path, err)
		}
	}
	if !fake.container.State.Running {
		t.Fatal("rollback lost running container")
	}
	if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatalf("completed rollback left journal: %v", err)
	}
}

func TestPrivateReadsRejectInvalidTypesPermissionsAndSizes(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "data")
	if err := os.WriteFile(path, []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readPrivateLimit(path, 4); err != nil || string(data) != "1234" {
		t.Fatalf("exact limit rejected: %q %v", data, err)
	}
	if _, err := readPrivateLimit(path, 3); err == nil {
		t.Fatal("oversized private file accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path); err == nil {
		t.Fatal("public permissions accepted")
	}
	fifo := filepath.Join(base, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(fifo); err == nil {
		t.Fatal("FIFO accepted")
	}
	if _, err := readPrivate(base); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestSnapshotStreamingKeepsStrictDecodingAndSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	for _, data := range []string{`[{"path":"test","unexpected":true}]`, `[] []`, `[] invalid`} {
		if err := atomicWrite(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var snapshots []fileSnapshot
		if err := loadSnapshot(path, &snapshots); err == nil {
			t.Fatalf("invalid snapshot accepted: %s", data)
		}
	}
	if err := atomicWrite(path, []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var snapshots []fileSnapshot
	if err := loadSnapshot(path, &snapshots); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, (64<<20)+1); err != nil {
		t.Fatal(err)
	}
	if err := loadSnapshot(path, &snapshots); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("oversized snapshot accepted: %v", err)
	}
}

func TestBrokenTLSLandingCanBeDisabledAndRemoved(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		for _, damage := range []string{"missing", "corrupt"} {
			t.Run(fmt.Sprintf("disabled=%t/%s", disabled, damage), func(t *testing.T) {
				m, fake := deployed(t)
				ctx := context.Background()
				if _, err := m.AddLanding(ctx, "offline", "tls", 9443); err != nil {
					t.Fatal(err)
				}
				if disabled {
					if err := m.ChangeLink(ctx, "landing", "offline", "disable"); err != nil {
						t.Fatal(err)
					}
				}
				s, err := m.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				key := s.Node.LandingAccesses[0].KeyFile
				if damage == "missing" {
					err = os.Remove(key)
				} else {
					err = atomicWrite(key, []byte("corrupt key"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !disabled {
					if err := m.ChangeLink(ctx, "landing", "offline", "disable"); err != nil {
						t.Fatal(err)
					}
				}
				unchanged := savedOperationFiles(t, m)
				if err := m.ChangeLink(ctx, "landing", "offline", "enable"); err == nil {
					t.Fatal("broken certificate enabled")
				}
				unchanged()
				if err := m.ChangeLink(ctx, "landing", "offline", "remove"); err != nil {
					t.Fatal(err)
				}
				s, err = m.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				if len(s.Node.LandingAccesses) != 0 || !fake.container.State.Running || bytes.Contains(readFile(t, m.paths.Config()), []byte("offline")) {
					t.Fatal("landing removal left configuration or stopped the primary service")
				}
			})
		}
	}
}

func TestUnchangedSettingsSkipBackupMaintenance(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	s, config, err := m.managedConfig()
	if err != nil {
		t.Fatal(err)
	}
	level, err := m.codec.CurrentLogLevel(config)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(m.paths.Staging(), "candidate-123.json")
	if err := atomicWrite(stale, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(m.paths.Backups(), "imported-invalid", "snapshot.json")
	if err := atomicWrite(bad, []byte("invalid snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	m.errOut = &output
	if err := m.PatchSetting(ctx, "log", level); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPolicy(ctx, s.Node.AccessPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("unchanged operation ran maintenance: %v", err)
	}
	if strings.Contains(output.String(), "清理残留") {
		t.Fatalf("unchanged operation parsed backups: %s", output.String())
	}
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("explicit recovery skipped maintenance: %v", err)
	}
	if !strings.Contains(output.String(), "清理残留") {
		t.Fatal("invalid snapshot did not stop certificate cleanup")
	}
}

func TestBackupPruningRetainsReferencesFromNewestSnapshots(t *testing.T) {
	m, _ := deployed(t)
	snaps := managedSnapshots(t, m)
	// Keep these newer than deployment backups regardless of the calendar date.
	stamp := m.now().UTC().Add(time.Second)
	for i := 0; i < 6; i++ {
		copy := append([]fileSnapshot(nil), snaps...)
		for j := range copy {
			if copy[j].Path == m.paths.Config() {
				config := configRoot(t, copy[j].Data)
				config["certificateReference"] = fmt.Sprintf("/etc/xray/tls/cert-%d/cert.pem", i)
				var err error
				copy[j].Data, err = json.Marshal(config)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		name := stamp.Add(time.Duration(i)*time.Nanosecond).Format("20060102T150405.000000000Z") + "-aaaaaaaaaaaa"
		if err := saveJSON(filepath.Join(m.paths.Backups(), name, "snapshot.json"), copy); err != nil {
			t.Fatal(err)
		}
	}
	refs, err := m.pruneBackups()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if refs[fmt.Sprintf("cert-%d", i)] != (i >= 3) {
			t.Fatalf("unexpected reference retention: %v", refs)
		}
	}
	entries, err := os.ReadDir(m.paths.Backups())
	if err != nil || len(entries) != 3 {
		t.Fatalf("backup retention: %d %v", len(entries), err)
	}
}
