package docker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelayManagementMenuActions(t *testing.T) {
	for _, action := range []string{"native", "clash", "test", "update", "rotate", "disable", "remove"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			ctx := context.Background()
			m, _ := deployed(t)
			peer := upstreamPeer(t, m)
			if err := m.AddRelay(ctx, "office", peer); err != nil {
				t.Fatal(err)
			}
			s, _ := m.LoadState()
			old := s.Node.RelayLinks[0]
			answers := []string{"2\n", "1\n"}
			want := ""
			switch action {
			case "native", "clash":
				format := "1\n"
				if action == "clash" {
					format = "2\n"
				}
				answers = append(answers, "1\n", format)
				want = old.UUID
			case "test":
				answers = append(answers, "2\n")
				want = "落地端点 TCP 可达"
			case "update":
				peer.Server = "updated.example.com"
				data, _ := landingText(peer)
				path := filepath.Join(t.TempDir(), "upstream.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				answers = append(answers, "3\n", "3\n", path+"\n", "y\n")
			case "rotate":
				answers = append(answers, "4\n", "y\n")
			case "disable":
				answers = append(answers, "5\n")
			case "remove":
				answers = append(answers, "6\n", "y\n")
			}
			answers = append(answers, "\n", "0\n")
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: answers, output: &out}
			if err := m.linkMenu(ctx, newUI(in, &out)); err != nil {
				t.Fatal(err)
			}
			result := in.screens[len(in.screens)-2]
			if in.index != len(answers) || !strings.Contains(result, "按 Enter 返回菜单") || want != "" && !strings.Contains(result, want) {
				t.Fatalf("%s result disappeared or navigation failed: %s", action, result)
			}
			if !strings.Contains(out.String(), "当前中转") || !strings.Contains(out.String(), "office-out") || !strings.Contains(out.String(), "客户端用户") {
				t.Fatalf("relay detail card is incomplete: %s", out.String())
			}
			s, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			if action == "remove" {
				if len(s.Node.RelayLinks) != 0 {
					t.Fatal("relay was not removed")
				}
				return
			}
			got := s.Node.RelayLinks[0]
			if action == "update" && (got.Upstream != peer || got.UUID != old.UUID) || action == "rotate" && got.UUID == old.UUID || action == "disable" && got.Enabled {
				t.Fatalf("menu applied the wrong change: %+v", got)
			}
		})
	}
}

func TestLinkMenuRetainsTCPErrorUntilReturn(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	ctx := context.Background()
	if err := m.AddRelay(ctx, "office", upstreamPeer(t, m)); err != nil {
		t.Fatal(err)
	}
	m.reachable = func(context.Context, string, int) error { return errors.New("connection refused") }
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "1\n", "2\n", "\n", "0\n"}, output: &out}
	if err := m.linkMenu(ctx, newUI(in, &out)); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 5 || !strings.Contains(in.screens[3], "落地端点不可达") || !strings.Contains(in.screens[3], "按 Enter 返回菜单") {
		t.Fatalf("TCP error was lost: %s", out.String())
	}
}

func TestLandingPasteCleansAllEditorFiles(t *testing.T) {
	for _, outcome := range []string{"success", "invalid-json", "editor-error"} {
		t.Run(outcome, func(t *testing.T) {
			m, _ := deployed(t)
			peer := upstreamPeer(t, m)
			data, _ := landingText(peer)
			var dir string
			m.runEditor = func(ctx context.Context, editor, path string) error {
				dir = filepath.Dir(path)
				fileInfo, err := os.Stat(path)
				if err != nil || fileInfo.Mode().Perm() != 0600 {
					t.Fatal("paste file is not private")
				}
				dirInfo, err := os.Stat(dir)
				if err != nil || dirInfo.Mode().Perm() != 0700 {
					t.Fatal("paste directory is not private")
				}
				// A concurrent manager must leave this editor's leased session alone.
				if err := m.cleanupLandingPasteDirs(); err != nil {
					return err
				}
				for _, name := range []string{"landing.json~", ".landing.json.swp"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("editor backup"), 0600); err != nil {
						return err
					}
				}
				if outcome == "editor-error" {
					return errors.New("editor failed")
				}
				if outcome == "invalid-json" {
					data = []byte("invalid json")
				}
				return os.WriteFile(path, data, 0600)
			}
			got, err := m.pasteLandingText(context.Background(), newUI(strings.NewReader(""), m.out))
			if outcome == "success" && (err != nil || got != peer) || outcome != "success" && err == nil {
				t.Fatalf("unexpected paste result: %+v, %v", got, err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("editor files remain after %s: %v", outcome, err)
			}
		})
	}
}

func TestLandingPasteReclaimsOnlyOwnedAbandonedSessions(t *testing.T) {
	m, _ := deployed(t)
	owned := filepath.Join(m.paths.Staging(), "landing-paste-123")
	unknown := filepath.Join(m.paths.Staging(), "landing-paste-124")
	for _, dir := range []string{owned, unknown} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "landing.json~"), []byte("saved credentials"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveJSON(filepath.Join(owned, ".proxydock-landing.json"), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupLandingPasteDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("abandoned private credentials remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unknown, "landing.json~")); err != nil {
		t.Fatalf("cleanup removed an unowned directory: %v", err)
	}
}

func TestManualTLSPeerAndLinkNameConflicts(t *testing.T) {
	m, _ := deployed(t)
	peer := upstreamPeer(t, m)
	input := strings.Join([]string{"tls-exit", "2", "2", "192.168.1.20", "8443", "tls.example.com", peer.UUID, strings.Repeat("a", 64), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}, "\n") + "\n"
	var out dockerTerminalOutput
	got, err := askLandingPeer(newUI(strings.NewReader(input), &out))
	if err != nil || got.Core != "sing-box" || got.Security != "tls" || got.PublicKey != "" || got.ShortID != "" {
		t.Fatalf("manual TLS import failed: %+v, %v", got, err)
	}
	if strings.Contains(out.String(), "REALITY 公钥") || strings.Contains(out.String(), "short ID") {
		t.Fatal("TLS input requested REALITY credentials")
	}
	s, _ := m.LoadState()
	for _, name := range []string{s.Node.UserName, s.Node.InboundTag, "ACCESS-custom", "DIRECT", "proxy"} {
		if err := m.AddRelay(context.Background(), name, peer); err == nil {
			t.Fatalf("conflicting name %q accepted", name)
		}
	}
	if _, err := m.AddLanding(context.Background(), "edge", "current", 0); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"EDGE", "landing-edge", "edge-in"} {
		if err := m.AddRelay(context.Background(), name, peer); err == nil {
			t.Fatalf("existing landing identity %q was reused", name)
		}
	}
}
