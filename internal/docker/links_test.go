package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func upstreamPeer(t *testing.T, m *Manager) model.LandingPeer {
	t.Helper()
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return model.LandingPeer{Name: "exit", Core: model.CoreXray, Security: "reality", Server: "upstream.example.com", Port: 443, SNI: s.Node.SNI, UUID: s.Node.UUID, PublicKey: s.Node.PublicKey, ShortID: s.Node.ShortID, Flow: model.VisionFlow}
}

func linkCLI(m *Manager, input string, args ...string) (string, error) {
	var out bytes.Buffer
	cmd := m.Command()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(append([]string{"config"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func assertSnapshots(t *testing.T, m *Manager, before []fileSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
		t.Fatal("failed operation changed managed files")
	}
}

func TestLandingTextFormatsAndValidation(t *testing.T) {
	m, _ := deployed(t)
	peer := upstreamPeer(t, m)
	for _, core := range []string{"xray", "sing-box"} {
		peer.Core = core
		for _, security := range []string{"reality", "tls"} {
			peer.Security = security
			if security == "tls" {
				peer.PublicKey, peer.ShortID = "", ""
				peer.CertificateSHA256 = strings.Repeat("a", 64)
				peer.CertificatePublicKeySHA256 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
			}
			bundle, _ := landingText(peer)
			bare, _ := jsonBytes(peer)
			forge, _ := jsonBytes(LandingBundle{ManagedBy: "proxyforge", Kind: "proxyforge-landing", SchemaVersion: 3, Peer: peer})
			for _, data := range [][]byte{bundle, bare, forge} {
				got, err := ParseLandingText(data)
				if err != nil || got != peer {
					t.Fatalf("%s/%s import failed: got %+v, %v", core, security, got, err)
				}
				if strings.Contains(string(data), "private_key") || strings.Contains(string(data), "key_file") {
					t.Fatal("connection text contains server-side key material")
				}
			}
		}
		peer = upstreamPeer(t, m)
	}
	good, _ := landingText(peer)
	for _, bad := range []string{
		"", "null", "[]", "{}", string(good) + "{}", strings.Repeat(" ", maxLandingTextSize+1),
		strings.Replace(string(good), `"schema_version": 1`, `"schema_version": 99`, 1),
		strings.Replace(string(good), `"managed_by": "proxydock"`, `"managed_by": "other"`, 1),
		strings.Replace(string(good), `"kind": "proxydock-landing"`, `"kind": "other"`, 1),
		strings.Replace(string(good), `"flow": "xtls-rprx-vision"`, `"flow": "other"`, 1),
		strings.Replace(string(good), `"public_key":`, `"private_key":`, 1),
	} {
		if _, err := ParseLandingText([]byte(bad)); err == nil {
			t.Fatalf("invalid connection text accepted: %q", bad[:min(len(bad), 100)])
		}
	}
	if _, err := readLandingInput("peer.json", true, strings.NewReader(string(good))); err == nil {
		t.Fatal("ambiguous input accepted")
	}
	if _, err := readLandingInput("", true, strings.NewReader(strings.Repeat(" ", maxLandingTextSize+1))); err == nil {
		t.Fatal("oversized stdin accepted")
	}
	path := filepath.Join(t.TempDir(), "peer.json")
	if err := os.WriteFile(path, good, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLandingInput(path, false, nil); err == nil {
		t.Fatal("publicly readable credential file accepted")
	}
}

func TestRelayCLIImportUpdateRotateAndClient(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	if _, err := linkCLI(m, "", "landing", "add", "nihao"); err != nil {
		t.Fatal(err)
	}
	text, err := linkCLI(m, "", "landing", "export", "nihao")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := ParseLandingText([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := m.LoadState()
	if peer.Port != s.Node.Port || peer.Security != s.Node.Security {
		t.Fatal("default landing CLI did not share the primary inbound")
	}
	peer.Server = "upstream.example.com"
	data, _ := landingText(peer)
	if _, err := linkCLI(m, string(data), "relay", "add", "office", "--upstream-stdin"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.LoadState()
	old := s.Node.RelayLinks[0]
	if old.UUID == s.Node.UUID || old.UUID == peer.UUID {
		t.Fatal("relay reused another user's credentials")
	}
	for _, format := range []string{"native", "clash"} {
		client, err := linkCLI(m, "", "relay", "client", "office", "--format", format)
		if err != nil || !strings.Contains(client, old.UUID) || !strings.Contains(client, s.Node.Server) {
			t.Fatalf("%s relay client is invalid: %v", format, err)
		}
	}
	if _, err := m.RelayClient(ctx, "native", "nihao"); err == nil {
		t.Fatal("relay client command exported a landing user's client")
	}
	if _, err := linkCLI(m, "", "relay", "test", "office"); err != nil {
		t.Fatal(err)
	}
	peer.Server, peer.Port = "new-upstream.example.com", 8443
	data, _ = landingText(peer)
	if _, err := linkCLI(m, string(data), "relay", "update", "office", "--upstream-stdin"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.LoadState()
	if got := s.Node.RelayLinks[0]; got.UUID != old.UUID || got.UserName != old.UserName || got.Enabled != old.Enabled || got.Upstream != peer {
		t.Fatalf("upstream update changed the local identity: %+v", got)
	}
	if config := string(readFile(t, m.paths.Config())); !strings.Contains(config, peer.Server) || strings.Contains(config, `"address": "upstream.example.com"`) {
		t.Fatal("upstream update left stale outbound settings")
	}
	if _, err := linkCLI(m, "", "relay", "rotate", "office"); err == nil {
		t.Fatal("noninteractive rotation did not require explicit confirmation")
	}
	if _, err := linkCLI(m, "", "relay", "rotate", "office", "--yes"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.LoadState()
	rotated := s.Node.RelayLinks[0]
	if rotated.UUID == old.UUID || rotated.Upstream != peer || !rotated.Enabled || strings.Contains(string(readFile(t, m.paths.Config())), old.UUID) {
		t.Fatal("relay rotation did not replace only the local UUID")
	}
	landingUUID := s.Node.LandingAccesses[0].UUID
	mainUUID := s.Node.UUID
	if _, err := linkCLI(m, "", "landing", "rotate", "nihao", "--yes"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.LoadState()
	if s.Node.LandingAccesses[0].UUID == landingUUID || s.Node.UUID != mainUUID || s.Node.RelayLinks[0].UUID != rotated.UUID {
		t.Fatal("landing rotation affected another user's credentials")
	}
}

func TestRelayReachabilityAndEndpointChecks(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	peer := upstreamPeer(t, m)
	before := managedSnapshots(t, m)
	m.reachable = func(context.Context, string, int) error { return errors.New("connection refused") }
	if err := m.AddRelay(ctx, "office", peer); !errors.Is(err, ErrRelayUnreachable) {
		t.Fatalf("unreachable endpoint was not reported: %v", err)
	}
	assertSnapshots(t, m, before)
	if err := m.AddRelay(ctx, "office", peer, RelayOptions{AllowUnreachable: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.TestRelay(ctx, "office"); !errors.Is(err, ErrRelayUnreachable) {
		t.Fatalf("TCP test failed to report unreachable upstream: %v", err)
	}
	for _, server := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:127.0.0.1", "203.0.113.1"} {
		bad := peer
		bad.Server = server
		if err := m.UpdateRelay(ctx, "office", bad, RelayOptions{AllowUnreachable: true}); err == nil {
			t.Fatalf("allow-unreachable bypassed address validation for %s", server)
		}
	}
	s, _ := m.LoadState()
	bad := peer
	bad.Server, bad.Port = s.Node.Server, s.Node.Port
	if err := m.UpdateRelay(ctx, "office", bad, RelayOptions{AllowUnreachable: true}); err == nil {
		t.Fatal("self-loop was accepted")
	}
	if err := validateRelayAddress("192.168.31.20"); err != nil {
		t.Fatalf("LAN test address rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := m.checkRelay(ctx, peer, RelayOptions{AllowUnreachable: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("allow-unreachable bypassed cancellation: %v", err)
	}
}

func TestLinkChangesRollbackAndPreserveDisabledState(t *testing.T) {
	for _, failure := range []string{"validation", "restart"} {
		for _, operation := range []string{"update", "rotate-relay", "rotate-landing"} {
			t.Run(failure+"/"+operation, func(t *testing.T) {
				m, fake := deployed(t)
				ctx := context.Background()
				peer := upstreamPeer(t, m)
				if err := m.AddRelay(ctx, "office", peer); err != nil {
					t.Fatal(err)
				}
				if _, err := m.AddLanding(ctx, "edge", "tls", 8444); err != nil {
					t.Fatal(err)
				}
				before := managedSnapshots(t, m)
				if failure == "validation" {
					fake.rejectValidation = true
				} else {
					fake.failRestart = 1
				}
				var err error
				switch operation {
				case "update":
					peer.Server = "new-upstream.example.com"
					err = m.UpdateRelay(ctx, "office", peer, RelayOptions{})
				case "rotate-relay":
					err = m.ChangeLink(ctx, "relay", "office", "rotate")
				case "rotate-landing":
					err = m.ChangeLink(ctx, "landing", "edge", "rotate")
				}
				if err == nil {
					t.Fatal("failed configuration change was accepted")
				}
				assertSnapshots(t, m, before)
			})
		}
	}
	m, _ := deployed(t)
	ctx := context.Background()
	peer := upstreamPeer(t, m)
	if err := m.AddRelay(ctx, "office", peer); err != nil {
		t.Fatal(err)
	}
	if err := m.ChangeLink(ctx, "relay", "office", "disable"); err != nil {
		t.Fatal(err)
	}
	s, _ := m.LoadState()
	old := s.Node.RelayLinks[0]
	peer.Port = 8443
	if err := m.UpdateRelay(ctx, "office", peer, RelayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.ChangeLink(ctx, "relay", "office", "rotate"); err != nil {
		t.Fatal(err)
	}
	s, _ = m.LoadState()
	got := s.Node.RelayLinks[0]
	if got.Enabled || got.UUID == old.UUID || got.Upstream != peer || !reflect.DeepEqual(got.UserName, old.UserName) {
		t.Fatal("updating or rotating a disabled relay enabled it or lost its state")
	}
	if _, err := m.RelayClient(ctx, "native", "office"); err == nil {
		t.Fatal("disabled relay exported an active client")
	}
}

func TestRelayUpdateKeepsWhitelistAndSwitchesUpstreamProtocol(t *testing.T) {
	m, _ := deployed(t)
	ctx := context.Background()
	peer := upstreamPeer(t, m)
	if err := m.AddRelay(ctx, "office", peer); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPolicy(ctx, model.AccessPolicy{Whitelist: []model.AccessEntry{{Type: "domain", Value: "allowed.example.com", Ports: "443"}}}); err != nil {
		t.Fatal(err)
	}
	peer.Core, peer.Security = "sing-box", "tls"
	peer.PublicKey, peer.ShortID = "", ""
	peer.CertificateSHA256 = strings.Repeat("a", 64)
	peer.CertificatePublicKeySHA256 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if err := m.UpdateRelay(ctx, "office", peer, RelayOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := m.ChangeLink(ctx, "relay", "office", "rotate"); err != nil {
		t.Fatal(err)
	}
	root := configRoot(t, readFile(t, m.paths.Config()))
	found := false
	for _, item := range root["outbounds"].([]any) {
		out := item.(map[string]any)
		if out["tag"] != "office-out" {
			continue
		}
		found = true
		stream := out["streamSettings"].(map[string]any)
		tls := stream["tlsSettings"].(map[string]any)
		if stream["security"] != "tls" || tls["allowInsecure"] != false || tls["pinnedPeerCertSha256"] != peer.CertificateSHA256 || stream["realitySettings"] != nil {
			t.Fatalf("TLS upstream did not use the pinned certificate: %+v", stream)
		}
	}
	if !found {
		t.Fatal("relay outbound was lost")
	}
	whiteAt, denyAt, relayAt := -1, -1, -1
	for i, item := range root["routing"].(map[string]any)["rules"].([]any) {
		rule := item.(map[string]any)
		switch rule["ruleTag"] {
		case "access-white-relay-office-domain-ports-1":
			whiteAt = i
			if rule["outboundTag"] != "office-out" || rule["port"] != "443" || !reflect.DeepEqual(rule["user"], []any{"relay-office"}) {
				t.Fatalf("relay whitelist scope changed: %+v", rule)
			}
		case "access-final":
			denyAt = i
		}
		if rule["outboundTag"] == "office-out" && rule["domain"] == nil {
			relayAt = i
		}
	}
	if whiteAt < 0 || denyAt <= whiteAt || relayAt <= denyAt {
		t.Fatalf("update or rotation bypassed whitelist ordering: white=%d deny=%d relay=%d", whiteAt, denyAt, relayAt)
	}
}
