package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestNetworkChoiceOrderAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, saved, selected, input, want string
		defaultChoice                      int
	}{
		{"new-default", "", "", "\n", networkPublish, 1},
		{"publish-first", "", "", "1\n", networkPublish, 1},
		{"host-second", "", "", "2\n", networkHost, 1},
		{"saved-host", networkHost, "", "\n", networkHost, 2},
		{"saved-publish", networkPublish, "", "\n", networkPublish, 1},
		{"selected-host", networkPublish, networkHost, "\n", networkHost, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := testManager(t)
			if tc.saved != "" {
				if err := m.prepareWithNetwork(context.Background(), "26.3.27", tc.saved, false); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			got, err := m.chooseDeployNetwork(newUI(strings.NewReader(tc.input), &out), tc.selected)
			if err != nil || got != tc.want {
				t.Fatalf("network choice = %q, %v; want %q", got, err, tc.want)
			}
			text := out.String()
			publish, host := strings.Index(text, "1   端口映射"), strings.Index(text, "2   宿主机网络")
			if publish < 0 || host <= publish || !strings.Contains(text, fmt.Sprintf("请选择 [%d]", tc.defaultChoice)) {
				t.Fatalf("unexpected network menu:\n%s", text)
			}
		})
	}
}

func TestCommandsDefaultToPublishedNetwork(t *testing.T) {
	for _, action := range []string{"prepare", "deploy"} {
		for _, explicitHost := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/host=%t", action, explicitHost), func(t *testing.T) {
				m, fake := testManager(t)
				args := []string{action, "--version", "26.3.27"}
				want := networkPublish
				if explicitHost {
					args = append(args, "--network", "host")
					want = networkHost
				}
				if action == "deploy" {
					args = append(args, "--security", "tls", "--server", "example.com", "--port", "8443")
				}
				cmd := m.Command()
				cmd.SetIn(strings.NewReader(""))
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				if err := cmd.ExecuteContext(context.Background()); err != nil {
					t.Fatal(err)
				}
				md, err := m.metadata()
				if err != nil || md.Network != want {
					t.Fatalf("network = %q, %v; want %q", md.Network, err, want)
				}
				if action == "prepare" {
					if _, err := os.Stat(m.paths.Compose()); !os.IsNotExist(err) {
						t.Fatalf("prepare wrote Compose: %v", err)
					}
					return
				}
				compose := string(readFile(t, m.paths.Compose()))
				if strings.Contains(compose, "network_mode: host") != explicitHost {
					t.Fatalf("wrong compose network:\n%s", compose)
				}
				if action == "deploy" {
					if fake.container == nil || !fake.container.State.Running || (fake.container.HostConfig.NetworkMode == networkHost) != explicitHost {
						t.Fatalf("wrong deployed network: %+v", fake.container)
					}
					if !explicitHost && !strings.Contains(compose, `"8443:8443"`) {
						t.Fatalf("default deployment did not publish port:\n%s", compose)
					}
				}
			})
		}
	}
}

func TestCommandsKeepSavedNetworkWhenOmitted(t *testing.T) {
	for _, mode := range []string{networkHost, networkPublish} {
		for _, action := range []string{"deploy", "upgrade"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				m, fake := fallbackDeployment(t, mode, true)
				args := []string{action, "--version", "26.3.27"}
				if action == "deploy" {
					args = append(args, "--server", "example.com", "--force")
				}
				cmd := m.Command()
				cmd.SetIn(strings.NewReader(""))
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				if err := cmd.ExecuteContext(context.Background()); err != nil {
					t.Fatal(err)
				}
				md, err := m.metadata()
				if err != nil || md.Network != mode || fake.container == nil || (fake.container.HostConfig.NetworkMode == networkHost) != (mode == networkHost) {
					t.Fatalf("saved network changed: %+v, container=%+v, error=%v", md, fake.container, err)
				}
			})
		}
	}
}

func TestHostComposeKeepsHostNetwork(t *testing.T) {
	m, _ := deployed(t)
	b := readFile(t, m.paths.Compose())
	if !strings.Contains(string(b), "network_mode: host") || strings.Contains(string(b), "\n    ports:\n") {
		t.Fatalf("host compose:\n%s", b)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Network != networkHost {
		t.Fatalf("default network = %q", md.Network)
	}
}

func TestPublishComposeKeepsFallbackInternal(t *testing.T) {
	m, f := testManager(t)
	if err := m.prepareWithNetwork(context.Background(), "26.3.27", networkPublish, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.paths.Compose()); !os.IsNotExist(err) {
		t.Fatalf("prepare wrote Compose before config: %v", err)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443"}, true); err != nil {
		t.Fatal(err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	compose := string(readFile(t, m.paths.Compose()))
	if strings.Contains(compose, "network_mode: host") || !strings.Contains(compose, "\n      - \"8443:8443\"\n") {
		t.Fatalf("publish compose:\n%s", compose)
	}
	_, ports, err := parseComposeNetwork([]byte(compose))
	if err != nil || len(ports) != 1 || ports[0].Host != 8443 {
		t.Fatalf("published ports = %+v, %v", ports, err)
	}
	if !state.Node.XrayFallbackGuard || state.Node.XrayFallbackPort == 0 {
		t.Fatal("internal fallback was disabled")
	}
	var config struct {
		Inbounds []struct {
			Listen         string
			Port           int
			Tag            string
			StreamSettings struct{ RealitySettings struct{ Target string } }
		}
	}
	if err = json.Unmarshal(readFile(t, m.paths.Config()), &config); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, inbound := range config.Inbounds {
		if inbound.Tag == "fallback-in" {
			found = inbound.Listen == "127.0.0.1" && inbound.Port == state.Node.XrayFallbackPort
		}
		if inbound.Port == state.Node.Port && inbound.StreamSettings.RealitySettings.Target != fmt.Sprintf("127.0.0.1:%d", state.Node.XrayFallbackPort) {
			t.Fatalf("REALITY target = %s", inbound.StreamSettings.RealitySettings.Target)
		}
	}
	if !found {
		t.Fatal("fallback listener must remain on container loopback")
	}
	if f.container == nil || f.container.HostConfig.NetworkMode == networkHost {
		t.Fatalf("container network = %#v", f.container)
	}
	if err = m.checkContainerNetwork(f.container); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Network != networkPublish {
		t.Fatalf("metadata network = %q", md.Network)
	}
}

func TestSetNetworkSwitchesAndRestoresHost(t *testing.T) {
	m, f := deployed(t)
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetNetwork(context.Background(), "publish"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readFile(t, m.paths.Compose())), "network_mode: host") {
		t.Fatal("still using host network")
	}
	if f.container.HostConfig.NetworkMode == networkHost {
		t.Fatal("container stayed on host network")
	}
	if err = m.SetNetwork(context.Background(), "publish"); err != nil {
		t.Fatal(err)
	}
	next, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if next.Node.UUID != state.Node.UUID || next.Node.Port != state.Node.Port {
		t.Fatalf("network switch changed node: %+v", next.Node)
	}
	if err = m.SetNetwork(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readFile(t, m.paths.Compose())), "network_mode: host") {
		t.Fatal("did not return to host network")
	}
	if f.container.HostConfig.NetworkMode != networkHost {
		t.Fatalf("container network = %s", f.container.HostConfig.NetworkMode)
	}
	if err = m.SetNetwork(context.Background(), "bridge"); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("invalid network error = %v", err)
	}
}

func TestDamagedComposeDoesNotDecideOwnership(t *testing.T) {
	m, f := deployed(t)
	good := readFile(t, m.paths.Compose())
	if err := os.WriteFile(m.paths.Compose(), []byte("broken compose"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.checkContainerNetwork(f.container); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.paths.Compose(), good, 0600); err != nil {
		t.Fatal(err)
	}
	f.container.HostConfig.NetworkMode = Project + "_default"
	if err := m.checkContainerNetwork(f.container); err == nil {
		t.Fatal("host compose accepted a bridged container")
	}
}

func fallbackDeployment(t *testing.T, mode string, running bool) (*Manager, *simulatedDocker) {
	t.Helper()
	m, f := testManager(t)
	if err := m.prepareWithNetwork(context.Background(), "26.3.27", mode, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", XrayFallbackPort: 62000}, true); err != nil {
		t.Fatal(err)
	}
	if !running {
		if _, err := m.Service(context.Background(), "stop"); err != nil {
			t.Fatal(err)
		}
	}
	return m, f
}

func managedSnapshots(t *testing.T, m *Manager) []fileSnapshot {
	t.Helper()
	var snapshots []fileSnapshot
	for _, path := range m.managedFiles() {
		s, err := snapshot(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots
}

func assertRestoredNetwork(t *testing.T, m *Manager, f *simulatedDocker, before []fileSnapshot, running bool) {
	t.Helper()
	if !reflect.DeepEqual(before, managedSnapshots(t, m)) {
		t.Fatal("recovery changed the original managed files")
	}
	if f.container == nil || f.container.State.Running != running {
		t.Fatalf("recovered runtime = %+v", f.container)
	}
	if _, err := m.inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatalf("recovery journal remains: %v", err)
	}
}

func TestNetworkFailuresRestoreFilesAndRuntime(t *testing.T) {
	for _, change := range []string{"host-to-publish", "publish-to-host", "publish-port"} {
		for _, running := range []bool{true, false} {
			for _, failure := range []string{"compose-config", "recreate", "health"} {
				if failure == "health" && !running {
					continue
				}
				t.Run(fmt.Sprintf("%s/running=%t/%s", change, running, failure), func(t *testing.T) {
					mode := networkPublish
					if change == "host-to-publish" {
						mode = networkHost
					}
					m, f := fallbackDeployment(t, mode, running)
					before := managedSnapshots(t, m)
					switch failure {
					case "compose-config":
						f.failComposeConfig = 1
					case "recreate":
						f.failComposeUp = 1
					case "health":
						f.healthFailures = 1
					}
					var err error
					if change == "publish-port" {
						err = m.Generate(context.Background(), model.GenerateOptions{Port: 9443}, false)
					} else {
						next := networkHost
						if mode == networkHost {
							next = networkPublish
						}
						err = m.SetNetwork(context.Background(), next)
					}
					if err == nil || strings.Contains(err.Error(), "回滚未完成") {
						t.Fatalf("failure = %v", err)
					}
					assertRestoredNetwork(t, m, f, before, running)
				})
			}
		}
	}
}

func TestNetworkRecoveryAfterComposeRewrite(t *testing.T) {
	for _, change := range []string{"host-to-publish", "publish-to-host", "publish-port"} {
		for _, running := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/running=%t", change, running), func(t *testing.T) {
				mode := networkPublish
				if change == "host-to-publish" {
					mode = networkHost
				}
				m, f := fallbackDeployment(t, mode, running)
				before := managedSnapshots(t, m)
				if _, err := m.begin("upgrade", f.container); err != nil {
					t.Fatal(err)
				}
				md, err := m.metadata()
				if err != nil {
					t.Fatal(err)
				}
				s, err := m.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "host-to-publish":
					md.Network = networkPublish
				case "publish-to-host":
					md.Network = networkHost
				case "publish-port":
					s.Node.Port = 9443
				}
				if _, err = m.writeManagedCompose(&md, &s.Node); err != nil {
					t.Fatal(err)
				}
				if _, err = m.inspect(context.Background()); err == nil {
					t.Fatal("ordinary inspection accepted network drift")
				}
				// The first recovery fails after restoring files; the journal must
				// remain usable even though the disk and runtime changed again.
				f.failComposeUp = 1
				if err = m.Recover(context.Background()); err == nil {
					t.Fatal("recovery failure concealed")
				}
				if _, err = os.Stat(m.paths.Journal()); err != nil {
					t.Fatal("recovery erased journal")
				}
				if err = m.Recover(context.Background()); err != nil {
					t.Fatal(err)
				}
				assertRestoredNetwork(t, m, f, before, running)
			})
		}
	}
}

func installLegacyPublish(t *testing.T, m *Manager, f *simulatedDocker, running bool) {
	t.Helper()
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the old generated mappings to verify they are rejected.
	b, err := renderCompose(md.ImageRef, networkPublish, []publishedPort{
		{Host: 8443, Container: 8443},
		{HostIP: "127.0.0.1", Host: 62000, Container: 62000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Compose(), b, 0600); err != nil {
		t.Fatal(err)
	}
	md.ComposeSHA256 = digest(b)
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	f.newContainer(md.ImageID)
	f.container.State.Running = running
}

func TestLegacyPublishIsRejectedWithoutMigration(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%t", running), func(t *testing.T) {
			m, f := fallbackDeployment(t, networkPublish, running)
			installLegacyPublish(t, m, f, running)
			before := managedSnapshots(t, m)
			f.calls = nil
			if _, err := m.Status(context.Background()); err == nil {
				t.Fatal("status accepted legacy compose")
			}
			if err := m.SetNetwork(context.Background(), networkPublish); err == nil {
				t.Fatal("network change accepted legacy compose")
			}
			if !reflect.DeepEqual(before, managedSnapshots(t, m)) || f.container.State.Running != running {
				t.Fatal("rejecting legacy compose changed files or running state")
			}
			requireNoRuntimeChanges(t, f)
		})
	}
}

func TestLegacyPublishRejectsTampering(t *testing.T) {
	for _, updateDigest := range []bool{true, false} {
		t.Run(fmt.Sprintf("updated-digest=%t", updateDigest), func(t *testing.T) {
			m, f := fallbackDeployment(t, networkPublish, true)
			installLegacyPublish(t, m, f, true)
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			b := append(readFile(t, m.paths.Compose()), []byte("# external change\n")...)
			if err = atomicWrite(m.paths.Compose(), b, 0600); err != nil {
				t.Fatal(err)
			}
			if updateDigest {
				md.ComposeSHA256 = digest(b)
				if err = saveJSON(m.paths.Metadata(), md); err != nil {
					t.Fatal(err)
				}
			}
			if err = m.SetNetwork(context.Background(), networkPublish); err == nil {
				t.Fatal("tampered legacy compose accepted")
			}
		})
	}
}

func TestRecoveryStillRejectsForeignContainer(t *testing.T) {
	for _, change := range []string{"label", "mount", "command", "writable-root"} {
		t.Run(change, func(t *testing.T) {
			m, f := fallbackDeployment(t, networkHost, true)
			if _, err := m.begin("upgrade", f.container); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "label":
				f.container.Config.Labels["io.proxydock.managed"] = "false"
			case "mount":
				f.container.Mounts[0].Source = "/foreign/config"
			case "command":
				f.container.Config.Cmd = []string{"foreign"}
			case "writable-root":
				f.container.HostConfig.ReadonlyRootfs = false
			}
			f.calls = nil
			if err := m.Recover(context.Background()); err == nil {
				t.Fatal("foreign container accepted by recovery")
			}
			for _, call := range f.calls {
				if contains(call, "stop") || contains(call, "up") || contains(call, "rm") {
					t.Fatalf("mutated foreign container: %v", call)
				}
			}
			if _, err := os.Stat(m.paths.Journal()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostPortChecksFollowNetwork(t *testing.T) {
	for _, mode := range []string{networkHost, networkPublish} {
		t.Run(mode, func(t *testing.T) {
			m, f := testManager(t)
			if err := m.prepareWithNetwork(context.Background(), "26.3.27", mode, false); err != nil {
				t.Fatal(err)
			}
			var freePorts []int
			m.portFree = func(port int) error { freePorts = append(freePorts, port); return nil }
			var checked [][]int
			health := m.healthCheck
			m.healthCheck = func(ctx context.Context, ports []int, image string) error {
				checked = append(checked, append([]int(nil), ports...))
				return health(ctx, ports, image)
			}
			if err := m.Generate(context.Background(), model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", XrayFallbackPort: 62000}, true); err != nil {
				t.Fatal(err)
			}
			want := []int{8443}
			if mode == networkHost {
				want = append(want, 62000)
			}
			if !reflect.DeepEqual(freePorts, want) {
				t.Fatalf("availability checks = %v, want %v", freePorts, want)
			}
			if _, err := m.Service(context.Background(), "stop"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Service(context.Background(), "start"); err != nil {
				t.Fatal(err)
			}
			if err := m.Upgrade(context.Background(), "26.4.1"); err != nil {
				t.Fatal(err)
			}
			if _, err := m.begin("upgrade", f.container); err != nil {
				t.Fatal(err)
			}
			if err := m.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(checked) != 4 {
				t.Fatalf("health checks = %v", checked)
			}
			for _, got := range checked {
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("health ports = %v, want %v", got, want)
				}
			}
			checked = nil
			next := networkHost
			if mode == networkHost {
				next = networkPublish
				want = []int{8443}
			} else {
				want = []int{8443, 62000}
			}
			if err := m.SetNetwork(context.Background(), next); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(checked, [][]int{want}) {
				t.Fatalf("network refresh checks = %v, want %v", checked, want)
			}
		})
	}
}

func TestHostPortsIncludeOnlyEnabledIndependentLandings(t *testing.T) {
	n := model.Node{Port: 8443, XrayFallbackGuard: true, XrayFallbackPort: 62000,
		LandingAccesses: []model.LandingAccess{
			{Security: model.LandingSecurityTLS, Port: 9443, Enabled: true},
			{Security: model.LandingSecurityTLS, Port: 10443, Enabled: false},
			{Security: model.LandingSecurityReality, Enabled: true},
		}}
	if got := hostPorts(n, networkPublish); !reflect.DeepEqual(got, []int{8443, 9443}) {
		t.Fatalf("publish ports = %v", got)
	}
	if got := hostPorts(n, networkHost); !reflect.DeepEqual(got, []int{8443, 9443, 62000}) {
		t.Fatalf("host ports = %v", got)
	}
}

func TestNetworkRollbackHealthFailureRetainsJournal(t *testing.T) {
	m, f := fallbackDeployment(t, networkHost, true)
	before := managedSnapshots(t, m)
	health := m.healthCheck
	m.healthCheck = func(context.Context, []int, string) error { return errors.New("persistent health failure") }
	if err := m.SetNetwork(context.Background(), networkPublish); err == nil || !strings.Contains(err.Error(), "回滚未完成") {
		t.Fatalf("failure = %v", err)
	}
	if _, err := os.Stat(m.paths.Journal()); err != nil {
		t.Fatal(err)
	}
	m.healthCheck = health
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertRestoredNetwork(t, m, f, before, true)
}
