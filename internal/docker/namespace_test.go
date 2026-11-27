package docker

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestDeploymentAndCleanupUseOnlyProxyDockNamespace(t *testing.T) {
	m, fake := testManager(t)
	oldFiles := []string{
		"/opt/proxyforge/xray-docker/compose.yaml",
		"/var/lib/proxyforge/docker/xray/owner.json",
		"/var/lib/proxyforge/docker/xray/transaction.json",
		"/var/lib/proxyforge/docker/xray-cleanup.json",
		"/run/proxyforge/xray-docker.lock",
	}
	for _, path := range oldFiles {
		if err := atomicWrite(m.paths.resolve(path), []byte("old instance data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if m.hasData() {
		t.Fatal("old directories were detected as ProxyDock data")
	}
	if err := m.Prepare(ctx, "26.3.27"); err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(ctx, model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true}, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/opt/proxydock/xray-docker/compose.yaml",
		"/opt/proxydock/xray-docker/config/config.json",
		"/var/lib/proxydock/docker/xray/owner.json",
		"/var/lib/proxydock/docker/xray/state.json",
		"/var/lib/proxydock/docker/xray/deployment.json",
		"/run/proxydock/xray-docker.lock",
	} {
		if _, err := os.Stat(m.paths.resolve(path)); err != nil {
			t.Fatalf("missing ProxyDock file %s: %v", path, err)
		}
	}
	compose := readFile(t, m.paths.Compose())
	if !bytes.Contains(compose, []byte("container_name: proxydock-xray")) || !bytes.Contains(compose, []byte("io.proxydock.managed")) || bytes.Contains(compose, []byte("proxyforge")) {
		t.Fatalf("incorrect deployment identity:\n%s", compose)
	}
	state, err := m.LoadState()
	if err != nil || state.ManagedBy != "proxydock" || state.Node.ManagedBy != "proxydock" {
		t.Fatalf("incorrect state ownership: %+v %v", state, err)
	}
	if err := m.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if m.hasData() {
		t.Fatal("ProxyDock cleanup left instance data")
	}
	for _, path := range oldFiles {
		if string(readFile(t, m.paths.resolve(path))) != "old instance data" {
			t.Fatalf("old instance file changed: %s", path)
		}
	}
	for _, call := range fake.calls {
		if strings.Contains(strings.Join(call, " "), "proxyforge") {
			t.Fatalf("Docker operation references old namespace: %v", call)
		}
	}
}

func TestProxyForgeOwnershipIsRejected(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		m, _ := prepared(t)
		if err := saveJSON(m.paths.Owner(), ownership{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance}); err != nil {
			t.Fatal(err)
		}
		if err := m.checkOwner(); err == nil {
			t.Fatal("old ownership accepted")
		}
	})
	t.Run("metadata", func(t *testing.T) {
		m, _ := prepared(t)
		md, err := m.metadata()
		if err != nil {
			t.Fatal(err)
		}
		md.ManagedBy = "proxyforge"
		if err := saveJSON(m.paths.Metadata(), md); err != nil {
			t.Fatal(err)
		}
		if _, err := m.metadata(); err == nil {
			t.Fatal("old metadata accepted")
		}
	})
	t.Run("state", func(t *testing.T) {
		m, _ := deployed(t)
		state, err := m.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		state.ManagedBy, state.Node.ManagedBy = "proxyforge", "proxyforge"
		if err := saveJSON(m.paths.State(), state); err != nil {
			t.Fatal(err)
		}
		if _, err := m.LoadState(); err == nil {
			t.Fatal("old state accepted")
		}
	})
	t.Run("container", func(t *testing.T) {
		m, fake := deployed(t)
		fake.container.Config.Labels = map[string]string{
			"io.proxyforge.managed": "true", "io.proxyforge.core": "xray", "io.proxyforge.instance": Instance,
			"com.docker.compose.project": "proxyforge-xray", "com.docker.compose.service": Service,
		}
		if err := m.checkOwnedIdentity(fake.container); err == nil {
			t.Fatal("old container ownership accepted")
		}
	})
	t.Run("cleanup-record", func(t *testing.T) {
		m, _ := prepared(t)
		if err := saveJSON(m.paths.CleanupRecord(), ownership{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.cleanupPending(); err == nil {
			t.Fatal("old cleanup record accepted")
		}
	})
	t.Run("certificate", func(t *testing.T) {
		m, _ := prepared(t)
		cert, err := m.newCertificate("main")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(cert.CertificateFile)
		if err := saveJSON(filepath.Join(dir, ".proxydock-certificate.json"), ownership{SchemaVersion: 1, ManagedBy: "proxyforge", InstanceID: Instance}); err != nil {
			t.Fatal(err)
		}
		if managedCertificateDirectory(dir) {
			t.Fatal("old certificate ownership accepted")
		}
	})
}
