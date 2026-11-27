package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"proxydock/internal/docker/model"
)

func TestRemovedAliasesFailBeforeDocker(t *testing.T) {
	m, fake := testManager(t)
	for _, version := range []string{"stable", "prerelease"} {
		if err := m.Prepare(context.Background(), version); err == nil {
			t.Fatalf("obsolete image selection accepted: %s", version)
		}
	}
	for _, format := range []string{"", "json", "mihomo", "yaml"} {
		if _, err := m.Client(context.Background(), format, ""); err == nil {
			t.Fatalf("obsolete client format accepted: %s", format)
		}
	}
	if len(fake.calls) != 0 || m.hasData() {
		t.Fatal("invalid aliases touched Docker or deployment data")
	}
}

func TestKeyPairRequiresCurrentXrayOutput(t *testing.T) {
	// https://github.com/XTLS/Xray-core/blob/main/main/commands/all/curve25519.go
	pair, err := parseKeyPair([]byte("PrivateKey: private\nPassword (PublicKey): public\nHash32: hash\n"))
	if err != nil || pair.Private != "private" || pair.Public != "public" {
		t.Fatalf("current output rejected: %+v %v", pair, err)
	}
	for _, output := range []string{
		"Private key: private\nPublic key: public\n",
		"PrivateKey: private\nPublicKey: public\n",
		"PrivateKey: private\nPassword: public\n",
	} {
		if _, err := parseKeyPair([]byte(output)); err == nil {
			t.Fatal("obsolete key output accepted")
		}
	}
}

func TestStateRequiresCurrentFields(t *testing.T) {
	for _, field := range []string{"security", "schema_version", "landing-security", "landing-port"} {
		t.Run(field, func(t *testing.T) {
			m, _ := deployed(t)
			state, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "security":
				state.Node.Security = ""
			case "schema_version":
				state.Node.SchemaVersion = 0
			case "landing-security":
				state.Node.LandingAccesses = []model.LandingAccess{{Name: "old", Port: 0}}
			case "landing-port":
				state.Node.LandingAccesses = []model.LandingAccess{{Name: "old", Security: "reality", Port: 12345}}
			}
			if err := saveJSON(m.paths.State(), state); err != nil {
				t.Fatal(err)
			}
			if _, err := m.LoadState(); err == nil {
				t.Fatalf("obsolete state accepted: %s", field)
			}
		})
	}
}

func TestMetadataRequiresCurrentNetworkAndChannel(t *testing.T) {
	for _, field := range []string{"network", "channel"} {
		t.Run(field, func(t *testing.T) {
			m, _ := prepared(t)
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			if field == "network" {
				md.Network = ""
			} else {
				md.Channel = "stable"
			}
			if err := saveJSON(m.paths.Metadata(), md); err != nil {
				t.Fatal(err)
			}
			if _, err := m.metadata(); err == nil {
				t.Fatalf("obsolete metadata accepted: %s", field)
			}
		})
	}
}

func TestCertificateWithoutOwnershipMarkerIsNotCollected(t *testing.T) {
	m, _ := deployed(t)
	cert, err := m.newCertificate("unmarked")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(cert.CertificateFile)
	if err := os.Remove(filepath.Join(dir, ".proxydock-certificate.json")); err != nil {
		t.Fatal(err)
	}
	if managedCertificateDirectory(dir) {
		t.Fatal("unmarked certificate was recognized as managed")
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cert.KeyFile); err != nil {
		t.Fatalf("unmarked certificate was deleted: %v", err)
	}
}
