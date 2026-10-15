package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"proxydock/internal/docker/model"
)

func accessEntry(t *testing.T, value string) model.AccessEntry {
	t.Helper()
	entry, err := model.ParseAccessEntry(value, false)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func accessCLI(m *Manager, args ...string) error {
	cmd := m.Command()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append([]string{"config", "access"}, args...))
	return cmd.ExecuteContext(context.Background())
}

func TestAccessCLIReadsPolicyAfterRecovery(t *testing.T) {
	m, f := deployed(t)
	ctx := context.Background()
	oldEntry := accessEntry(t, "old.example.com")
	pendingEntry := accessEntry(t, "pending.example.com")
	addedEntry := accessEntry(t, "added.example.com")
	if err := m.SetPolicy(ctx, model.AccessPolicy{Mode: "whitelist", Whitelist: []model.AccessEntry{oldEntry}}); err != nil {
		t.Fatal(err)
	}
	s, b, err := m.managedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.begin("config", f.container); err != nil {
		t.Fatal(err)
	}
	next, err := cloneNode(s.Node)
	if err != nil {
		t.Fatal(err)
	}
	next.AccessPolicy.Whitelist = []model.AccessEntry{pendingEntry}
	partial, err := m.codec.PatchAccessPolicy(b, s.Node, next)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Config(), partial, 0600); err != nil {
		t.Fatal(err)
	}
	next.ConfigSHA256 = digest(partial)
	if err = m.saveNode(next); err != nil {
		t.Fatal(err)
	}
	if err = accessCLI(m, "add", "white", addedEntry.Value); err != nil {
		t.Fatal(err)
	}
	final, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(final.Node.AccessPolicy.Whitelist, []model.AccessEntry{oldEntry, addedEntry}) {
		t.Fatalf("policy restored partial state: %+v", final.Node.AccessPolicy)
	}
}

func TestConcurrentAccessCLIUpdatesKeepBothEntries(t *testing.T) {
	m, f := deployed(t)
	other := New(Options{Root: m.paths.Root, Runner: f, RootCheck: m.rootCheck, LookPath: m.lookPath, HealthCheck: m.healthCheck})
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i, manager := range []*Manager{m, other} {
		wg.Add(1)
		go func(i int, manager *Manager) {
			defer wg.Done()
			<-start
			value := []string{"one.example.com", "two.example.com"}[i]
			errs <- accessCLI(manager, "add", "white", value)
		}(i, manager)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range s.Node.AccessPolicy.Whitelist {
		seen[entry.Value] = true
	}
	if len(seen) != 2 || !seen["one.example.com"] || !seen["two.example.com"] {
		t.Fatalf("lost concurrent update: %+v", s.Node.AccessPolicy)
	}
}

func TestInvalidAccessCLIDoesNotRecover(t *testing.T) {
	for _, args := range [][]string{{"mode", "invalid"}, {"switch", "invalid"}, {"clear", "invalid"}, {"add", "white", "https://invalid"}, {"remove", "invalid", "example.com"}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			m, f := deployed(t)
			if _, err := m.begin("config", f.container); err != nil {
				t.Fatal(err)
			}
			journal := readFile(t, m.paths.Journal())
			f.calls = nil
			if err := accessCLI(m, args...); err == nil {
				t.Fatal("invalid arguments accepted")
			}
			if len(f.calls) != 0 || !bytes.Equal(journal, readFile(t, m.paths.Journal())) {
				t.Fatal("invalid arguments recovered transaction")
			}
		})
	}
}

func TestTLSLandingSameNameCanBeRecreated(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(map[bool]string{true: "running", false: "stopped"}[running], func(t *testing.T) {
			m, f := fallbackDeployment(t, networkPublish, running)
			ctx := context.Background()
			if _, err := m.AddLanding(ctx, "office", "tls", 9443); err != nil {
				t.Fatal(err)
			}
			s, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			old := s.Node.LandingAccesses[0]
			if err = m.ChangeLink(ctx, "landing", "office", "remove"); err != nil {
				t.Fatal(err)
			}
			before := managedSnapshots(t, m)
			f.failComposeConfig = 1
			if _, err = m.AddLanding(ctx, "office", "tls", 9443); err == nil {
				t.Fatal("failed re-add unexpectedly succeeded")
			}
			assertRestoredNetwork(t, m, f, before, true)
			if _, err = m.AddLanding(ctx, "office", "tls", 9443); err != nil {
				t.Fatal(err)
			}
			s, err = m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			next := s.Node.LandingAccesses[0]
			if next.UUID == old.UUID || next.CertificateFile == old.CertificateFile || next.CertificateSHA256 == old.CertificateSHA256 {
				t.Fatal("recreated access retained old credentials")
			}
			if err = m.validateCertificate(old); err != nil {
				t.Fatalf("old backup certificate lost: %v", err)
			}
			if _, err = m.renderNode(s.Node); err != nil {
				t.Fatal(err)
			}
			if _, err = m.Client(ctx, "native", "office"); err != nil {
				t.Fatal(err)
			}
			if _, err = m.Peer("office"); err != nil {
				t.Fatal(err)
			}
			if !f.container.State.Running {
				t.Fatal("re-add did not start the container")
			}
		})
	}
}

func editedInbound(t *testing.T, root map[string]any, tag string) map[string]any {
	t.Helper()
	for _, v := range root["inbounds"].([]any) {
		in := v.(map[string]any)
		if in["tag"] == tag {
			return in
		}
	}
	t.Fatalf("inbound %s missing", tag)
	return nil
}

func TestEditingProtectsManagedLandingsAndFallback(t *testing.T) {
	cases := map[string]func(*testing.T, map[string]any){
		"landing port": func(t *testing.T, root map[string]any) { editedInbound(t, root, "office-in")["port"] = 10443 },
		"landing UUID": func(t *testing.T, root map[string]any) {
			editedInbound(t, root, "office-in")["settings"].(map[string]any)["clients"].([]any)[0].(map[string]any)["id"] = "00000000-0000-4000-8000-000000000000"
		},
		"landing certificate": func(t *testing.T, root map[string]any) {
			editedInbound(t, root, "office-in")["streamSettings"].(map[string]any)["tlsSettings"].(map[string]any)["certificates"].([]any)[0].(map[string]any)["certificateFile"] = "/etc/xray/tls/foreign/cert.pem"
		},
		"landing listen": func(t *testing.T, root map[string]any) { editedInbound(t, root, "office-in")["listen"] = "127.0.0.1" },
		"landing transport": func(t *testing.T, root map[string]any) {
			editedInbound(t, root, "office-in")["streamSettings"].(map[string]any)["network"] = "ws"
		},
		"fallback port":   func(t *testing.T, root map[string]any) { editedInbound(t, root, "fallback-in")["port"] = 62001 },
		"fallback listen": func(t *testing.T, root map[string]any) { editedInbound(t, root, "fallback-in")["listen"] = "0.0.0.0" },
		"fallback target": func(t *testing.T, root map[string]any) {
			editedInbound(t, root, "fallback-in")["settings"].(map[string]any)["address"] = "different.example.com"
		},
		"shared landing UUID": func(t *testing.T, root map[string]any) {
			for _, value := range editedInbound(t, root, "docker-one")["settings"].(map[string]any)["clients"].([]any) {
				client := value.(map[string]any)
				if client["email"] == "landing-shared" {
					client["id"] = "00000000-0000-4000-8000-000000000000"
				}
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m, f := fallbackDeployment(t, networkPublish, false)
			ctx := context.Background()
			if _, err := m.AddLanding(ctx, "office", "tls", 9443); err != nil {
				t.Fatal(err)
			}
			if _, err := m.AddLanding(ctx, "shared", "current", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Service(ctx, "stop"); err != nil {
				t.Fatal(err)
			}
			before := managedSnapshots(t, m)
			root := configRoot(t, readFile(t, m.paths.Config()))
			change(t, root)
			data, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ApplyEdited(ctx, data); err == nil {
				t.Fatal("managed endpoint edit accepted")
			}
			assertRestoredNetwork(t, m, f, before, false)
		})
	}
}

func TestEditingRetainsUnrelatedFieldsWithRecreatedTLSLanding(t *testing.T) {
	m, _ := fallbackDeployment(t, networkPublish, false)
	ctx := context.Background()
	if _, err := m.AddLanding(ctx, "office", "tls", 9443); err != nil {
		t.Fatal(err)
	}
	if err := m.ChangeLink(ctx, "landing", "office", "remove"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddLanding(ctx, "office", "tls", 9443); err != nil {
		t.Fatal(err)
	}
	root := configRoot(t, readFile(t, m.paths.Config()))
	root["log"] = map[string]any{"loglevel": "debug"}
	root["dns"] = map[string]any{"servers": []any{"1.1.1.1"}}
	root["inbounds"] = append(root["inbounds"].([]any), map[string]any{"tag": "custom-socks", "listen": "127.0.0.1", "port": 10808, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}})
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ApplyEdited(ctx, data); err != nil {
		t.Fatal(err)
	}
	actual := configRoot(t, readFile(t, m.paths.Config()))
	if !reflect.DeepEqual(configRoot(t, data), actual) {
		t.Fatal("unrelated edited fields changed")
	}
}

func TestServiceStartAndRestartRejectImageMismatch(t *testing.T) {
	for _, action := range []string{"start", "restart"} {
		t.Run(action, func(t *testing.T) {
			m, f := deployed(t)
			f.container.Image = "sha256:" + strings.Repeat("b", 64)
			before := managedSnapshots(t, m)
			f.calls = nil
			if _, err := m.Service(context.Background(), action); err == nil || !strings.Contains(err.Error(), "镜像") {
				t.Fatalf("image mismatch accepted: %v", err)
			}
			if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
				t.Fatal("image mismatch changed files")
			}
			for _, call := range f.calls {
				if contains(call, "restart") || contains(call, "up") || contains(call, "run") {
					t.Fatalf("image mismatch mutated runtime: %v", call)
				}
			}
		})
	}
}

func TestReadOnlyPeerAndListsBlockPendingTransaction(t *testing.T) {
	for _, operation := range []string{"peer", "access", "landing", "relay"} {
		t.Run(operation, func(t *testing.T) {
			m, f := deployed(t)
			if _, err := m.AddLanding(context.Background(), "office", "current", 0); err != nil {
				t.Fatal(err)
			}
			if _, err := m.begin("config", f.container); err != nil {
				t.Fatal(err)
			}
			before := managedSnapshots(t, m)
			journal := readFile(t, m.paths.Journal())
			f.calls = nil
			var err error
			if operation == "peer" {
				_, err = m.Peer("office")
			} else {
				cmd := m.Command()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"config", operation, "list"})
				err = cmd.ExecuteContext(context.Background())
			}
			if err == nil || !strings.Contains(err.Error(), "recover") {
				t.Fatalf("pending transaction was read: %v", err)
			}
			if len(f.calls) != 0 || !bytes.Equal(journal, readFile(t, m.paths.Journal())) || !reflect.DeepEqual(before, managedSnapshots(t, m)) {
				t.Fatal("read-only operation changed pending transaction")
			}
		})
	}
}

func TestStatusReportsInterruptedNetworkAsRecoverable(t *testing.T) {
	m, f := deployed(t)
	if _, err := m.begin("upgrade", f.container); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.Network = networkPublish
	if _, err = m.writeManagedCompose(&md, nil); err != nil {
		t.Fatal(err)
	}
	before := managedSnapshots(t, m)
	journal := readFile(t, m.paths.Journal())
	status, err := m.Status(context.Background())
	if err != nil || status.Label != "需恢复" {
		t.Fatalf("pending network: %+v, %v", status, err)
	}
	if !reflect.DeepEqual(before, managedSnapshots(t, m)) || !bytes.Equal(journal, readFile(t, m.paths.Journal())) {
		t.Fatal("status changed pending transaction")
	}
	f.container.Config.Labels["io.proxydock.managed"] = "false"
	if _, err = m.Status(context.Background()); err == nil {
		t.Fatal("pending transaction hid foreign container")
	}
	if _, err = os.Stat(m.paths.Journal()); err != nil {
		t.Fatal(err)
	}
}
