package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxydock/internal/docker/model"
)

func countDockerCalls(f *simulatedDocker, match func([]string) bool) int {
	count := 0
	for _, call := range f.calls {
		if match(call) {
			count++
		}
	}
	return count
}

func requireNoRuntimeChanges(t *testing.T, f *simulatedDocker) {
	t.Helper()
	for _, call := range f.calls {
		if call[0] == "run" || call[0] == "container" && (call[1] == "stop" || call[1] == "rm") || call[0] == "compose" && (contains(call, "up") || contains(call, "restart") || contains(call, "stop")) {
			t.Fatalf("unexpected container operation: %v", call)
		}
	}
}

func savedOperationFiles(t *testing.T, m *Manager) func() {
	t.Helper()
	saved := map[string][]byte{}
	for _, path := range m.managedFiles() {
		saved[path] = readFile(t, path)
	}
	entries, err := os.ReadDir(m.paths.Backups())
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		for path, before := range saved {
			if !bytes.Equal(before, readFile(t, path)) {
				t.Fatalf("unchanged operation wrote %s", path)
			}
		}
		after, err := os.ReadDir(m.paths.Backups())
		if err != nil || len(after) != len(entries) {
			t.Fatalf("unchanged operation created a backup: %v", err)
		}
		for i := range entries {
			if entries[i].Name() != after[i].Name() {
				t.Fatal("unchanged operation rotated backups")
			}
		}
	}
}

func TestClientExportWorksWithoutDockerOrCompose(t *testing.T) {
	for _, format := range []string{"clash", "native"} {
		t.Run(format, func(t *testing.T) {
			m, fake := deployed(t)
			if err := os.WriteFile(m.paths.Compose(), []byte("broken compose"), 0600); err != nil {
				t.Fatal(err)
			}
			m.lookPath = func(string) (string, error) { return "", errors.New("not installed") }
			fake.unavailable, fake.calls = true, nil
			b, err := m.Client(context.Background(), format, "")
			if err != nil || !bytes.Contains(b, []byte("example.com")) {
				t.Fatalf("local export failed: %s %v", b, err)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("export called Docker: %v", fake.calls)
			}
		})
	}
}

func TestReadOnlyOperationsLeavePendingTransactionUntouched(t *testing.T) {
	for _, operation := range []string{"clash", "native", "validate"} {
		t.Run(operation, func(t *testing.T) {
			m, fake := deployed(t)
			if _, err := m.begin("config", fake.container); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, m.paths.Journal())
			unchanged := savedOperationFiles(t, m)
			fake.calls = nil
			var err error
			if operation == "validate" {
				err = m.Validate(context.Background())
			} else {
				_, err = m.Client(context.Background(), operation, "")
			}
			if err == nil || !strings.Contains(err.Error(), "recover") || len(fake.calls) != 0 {
				t.Fatalf("read-only operation recovered transaction: %v %v", err, fake.calls)
			}
			if !bytes.Equal(before, readFile(t, m.paths.Journal())) || !fake.container.State.Running {
				t.Fatal("read-only operation changed recovery or runtime state")
			}
			unchanged()
		})
	}
}

func TestInvalidOperationsDoNotRecoverTransaction(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*Manager) error{
		"service":          func(m *Manager) error { _, err := m.Service(ctx, "invalid"); return err },
		"client":           func(m *Manager) error { _, err := m.Client(ctx, "invalid", ""); return err },
		"setting":          func(m *Manager) error { return m.PatchSetting(ctx, "invalid", "info") },
		"setting value":    func(m *Manager) error { return m.PatchSetting(ctx, "log", "invalid") },
		"link kind":        func(m *Manager) error { return m.ChangeLink(ctx, "invalid", "one", "enable") },
		"link action":      func(m *Manager) error { return m.ChangeLink(ctx, "relay", "one", "invalid") },
		"link name":        func(m *Manager) error { return m.ChangeLink(ctx, "relay", "../one", "enable") },
		"landing security": func(m *Manager) error { _, err := m.AddLanding(ctx, "one", "invalid", 0); return err },
		"landing port":     func(m *Manager) error { _, err := m.AddLanding(ctx, "one", "tls", 80); return err },
		"relay peer":       func(m *Manager) error { return m.AddRelay(ctx, "one", model.LandingPeer{}) },
		"policy":           func(m *Manager) error { return m.SetPolicy(ctx, model.AccessPolicy{Mode: "invalid"}) },
		"upgrade version":  func(m *Manager) error { return m.Upgrade(ctx, "invalid") },
		"prepare version":  func(m *Manager) error { return m.Prepare(ctx, "invalid") },
		"generate port":    func(m *Manager) error { return m.Generate(ctx, model.GenerateOptions{Port: -1}, true) },
		"generate server": func(m *Manager) error {
			return m.Generate(ctx, model.GenerateOptions{Server: "https://example.com"}, true)
		},
		"reset target": func(m *Manager) error { return m.Reset(ctx, model.ResetOptions{Target: "example.com"}) },
		"deploy port":  func(m *Manager) error { return m.Deploy(ctx, "latest", model.GenerateOptions{Port: -1}) },
		"edited JSON":  func(m *Manager) error { return m.ApplyEdited(ctx, []byte("broken JSON")) },
	}
	for name, operation := range cases {
		t.Run(name, func(t *testing.T) {
			m, fake := deployed(t)
			if _, err := m.begin("config", fake.container); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, m.paths.Journal())
			fake.calls = nil
			if err := operation(m); err == nil || len(fake.calls) != 0 {
				t.Fatalf("invalid operation reached Docker: %v %v", err, fake.calls)
			}
			if !bytes.Equal(before, readFile(t, m.paths.Journal())) || !fake.container.State.Running {
				t.Fatal("invalid operation recovered transaction")
			}
		})
	}
}

func TestUnchangedSettingsAndPolicyDoNotCallDocker(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	s, b, err := m.managedConfig()
	if err != nil {
		t.Fatal(err)
	}
	level, err := m.codec.CurrentLogLevel(b)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := savedOperationFiles(t, m)
	fake.calls = nil
	var errOut bytes.Buffer
	m.errOut = &errOut
	if err := m.PatchSetting(ctx, "log", level); err != nil {
		t.Fatal(err)
	}
	if err := m.SetPolicy(ctx, s.Node.AccessPolicy); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("unchanged settings reached Docker: %v", fake.calls)
	}
	text := errOut.String()
	if !strings.Contains(text, "配置未变化") || !strings.Contains(text, "访问策略未变化") || strings.Contains(text, "备份位置") || strings.Contains(text, "docker") {
		t.Fatalf("unchanged operation log: %s", text)
	}
	unchanged()
}

func TestUnchangedLinkDoesNotCallDocker(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	if _, err := m.AddLanding(ctx, "edge", "reality", 0); err != nil {
		t.Fatal(err)
	}
	unchanged := savedOperationFiles(t, m)
	fake.calls = nil
	if err := m.ChangeLink(ctx, "landing", "edge", "enable"); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("unchanged link reached Docker: %v", fake.calls)
	}
	unchanged()
}

func TestUnchangedUpgradePreservesFilesAndContainer(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "stopped"}[stopped], func(t *testing.T) {
			m, fake := deployed(t)
			ctx := context.Background()
			if stopped {
				if _, err := m.Service(ctx, "stop"); err != nil {
					t.Fatal(err)
				}
			}
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			before := *fake.container
			unchanged := savedOperationFiles(t, m)
			fake.calls = nil
			if err := m.Upgrade(ctx, md.RequestedVersion); err != nil {
				t.Fatal(err)
			}
			requireNoRuntimeChanges(t, fake)
			if !reflect.DeepEqual(before, *fake.container) {
				t.Fatal("same image changed container")
			}
			unchanged()
		})
	}
}

func TestSameImageChannelChangeDoesNotRecreateContainer(t *testing.T) {
	m, fake := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	fake.latestVersion, fake.latestImageID = md.RequestedVersion, md.ImageID
	fake.calls = nil
	if err := m.Upgrade(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	requireNoRuntimeChanges(t, fake)
	updated, err := m.metadata()
	if err != nil || updated.Channel != "latest" || updated.ImageID != md.ImageID || updated.DesiredState != "running" {
		t.Fatalf("same-image channel metadata: %+v %v", updated, err)
	}
}

func TestIdempotentServiceDoesNotValidateOrCreateBackup(t *testing.T) {
	for _, action := range []string{"start", "stop"} {
		t.Run(action, func(t *testing.T) {
			m, fake := deployed(t)
			ctx := context.Background()
			if action == "stop" {
				if _, err := m.Service(ctx, action); err != nil {
					t.Fatal(err)
				}
			}
			unchanged := savedOperationFiles(t, m)
			fake.calls = nil
			if _, err := m.Service(ctx, action); err != nil {
				t.Fatal(err)
			}
			requireNoRuntimeChanges(t, fake)
			unchanged()
		})
	}
}

func TestStopDoesNotRequireValidNodeConfig(t *testing.T) {
	m, fake := deployed(t)
	if err := os.WriteFile(m.paths.Config(), []byte("broken config"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Service(context.Background(), "stop"); err != nil {
		t.Fatal(err)
	}
	if fake.container.State.Running {
		t.Fatal("could not stop container with damaged config")
	}
}

func TestLogsDoNotDependOnComposeAndStillCheckOwnership(t *testing.T) {
	m, fake := deployed(t)
	if err := os.WriteFile(m.paths.Compose(), []byte("broken compose"), 0600); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	b, err := m.Service(context.Background(), "logs")
	if err != nil || !bytes.Contains(b, []byte("example log")) {
		t.Fatalf("diagnostic logs failed: %s %v", b, err)
	}
	for _, call := range fake.calls {
		if call[0] == "compose" || call[0] == "info" {
			t.Fatalf("logs used Compose/environment checks: %v", call)
		}
	}
	fake.container.Config.Labels["io.proxydock.managed"] = "false"
	fake.calls = nil
	if _, err := m.Service(context.Background(), "logs"); err == nil {
		t.Fatal("logs accepted a foreign container")
	}
	if countDockerCalls(fake, func(c []string) bool { return c[0] == "logs" }) != 0 {
		t.Fatal("read foreign container logs")
	}
}

func TestTLSLandingMenuSharesPrimaryPortAndCertificate(t *testing.T) {
	m, fake := prepared(t)
	ctx := context.Background()
	if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.addLandingInteractive(ctx, newUI(strings.NewReader("1\nedge\n"), io.Discard), s.Node); err != nil {
		t.Fatal(err)
	}
	peer, err := m.Peer("edge")
	if err != nil || peer.Security != "tls" || peer.Port != s.Node.Port || peer.CertificateSHA256 != s.Node.TLSCertificate.CertificateSHA256 {
		t.Fatalf("shared TLS peer: %+v %v", peer, err)
	}
	if err := validatePeer(peer); err != nil {
		t.Fatal(err)
	}
	updated, err := m.LoadState()
	if err != nil || !updated.Node.LandingAccesses[0].UsesPrimaryInbound() {
		t.Fatalf("TLS menu created an independent inbound: %+v %v", updated, err)
	}
	fake.calls = nil
	b, err := m.Client(ctx, "native", "edge")
	if err != nil || !bytes.Contains(b, []byte(peer.UUID)) || !bytes.Contains(b, []byte(peer.CertificateSHA256)) || len(fake.calls) != 0 {
		t.Fatalf("shared TLS client: %s %v %v", b, err, fake.calls)
	}
}

func TestPrepareGeneratesOneCapabilityKeyPair(t *testing.T) {
	_, fake := prepared(t)
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "run" && c[len(c)-1] == "x25519" }); count != 1 {
		t.Fatalf("capability keys generated %d times", count)
	}
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "container" && c[1] == "inspect" }); count != 0 {
		t.Fatalf("successful --rm containers triggered %d cleanup queries", count)
	}
}

func TestHealthCheckLooksUpContainerOnce(t *testing.T) {
	m, fake := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	var errOut bytes.Buffer
	m.errOut = &errOut
	ctx, cancel := context.WithTimeout(context.Background(), healthStabilityWindow+3*healthPollInterval)
	defer cancel()
	started := time.Now()
	if err := m.waitHealthy(ctx, nil, md.ImageID); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < healthStabilityWindow {
		t.Fatal("health check skipped the stability window")
	}
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "container" && c[1] == "ls" }); count != 1 {
		t.Fatalf("health check performed %d container lookups", count)
	}
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "container" && c[1] == "inspect" }); count < 2 || count > 7 {
		t.Fatalf("unexpected health polling count: %d", count)
	}
	if strings.Count(errOut.String(), "观察窗口") != 1 || strings.Contains(errOut.String(), "命令详情") || strings.Count(errOut.String(), "\n") > 3 {
		t.Fatalf("health log should be the observation line only:\n%s", errOut.String())
	}
}

func TestMenuStatusUsesOnlyContainerQueries(t *testing.T) {
	m, fake := deployed(t)
	fake.calls = nil
	st, err := m.menuStatus(context.Background())
	if err != nil || !st.Running || len(fake.calls) != 2 {
		t.Fatalf("lightweight status: %+v %v %v", st, err, fake.calls)
	}
	for _, call := range fake.calls {
		if call[0] != "container" {
			t.Fatalf("menu status called %v", call)
		}
	}
}

func TestEditorValidatesCandidateOnce(t *testing.T) {
	m, fake := deployed(t)
	_, b, err := m.managedConfig()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := m.codec.PatchLogLevel(b, "info")
	if err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncat > \"$1\" <<'CONFIG'\n"+string(candidate)+"CONFIG\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m.lookPath = func(name string) (string, error) {
		if name == "vim" {
			return editor, nil
		}
		return "/test/docker", nil
	}
	input, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	fake.calls = nil
	if err := m.Edit(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "run" }); count != 1 {
		t.Fatalf("editor validated %d times", count)
	}
	if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "compose" && c[1] == "version" }); count != 1 {
		t.Fatalf("editor prepared Docker %d times", count)
	}
	if !bytes.Equal(candidate, readFile(t, m.paths.Config())) {
		t.Fatal("editor did not apply candidate")
	}
}

func TestBlockedAddressPolicyUsesSharedRanges(t *testing.T) {
	for _, cidr := range model.BlockedDestinationCIDRs() {
		if !forbiddenAddress(netip.MustParsePrefix(cidr).Addr()) {
			t.Fatalf("missing blocked range %s", cidr)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if forbiddenAddress(netip.MustParseAddr(ip)) {
			t.Fatalf("blocked public address %s", ip)
		}
	}
	if !forbiddenAddress(netip.MustParseAddr("::ffff:192.168.1.1")) {
		t.Fatal("allowed mapped private address")
	}
}

type failedTemporaryContainerRunner struct {
	*simulatedDocker
	temporaryID string
	removed     bool
}

func (r *failedTemporaryContainerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := args[2:]
	if c[0] == "run" {
		return nil, context.Canceled
	}
	if c[0] == "container" && c[1] == "inspect" && strings.HasPrefix(c[2], Container+"-check-") {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		in := inspection{ID: r.temporaryID}
		in.Config.Labels = map[string]string{"io.proxydock.instance": Instance, "io.proxydock.role": "validator"}
		return json.Marshal([]inspection{in})
	}
	if c[0] == "container" && c[1] == "rm" {
		if c[len(c)-1] != r.temporaryID || ctx.Err() != nil {
			return nil, errors.New("removed wrong container or used canceled context")
		}
		r.removed = true
		return nil, nil
	}
	return r.simulatedDocker.Run(ctx, name, args...)
}

func TestFailedTemporaryContainerStillCleansUp(t *testing.T) {
	m, fake := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	runner := &failedTemporaryContainerRunner{simulatedDocker: fake, temporaryID: "temporary-validator-id"}
	m.runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.coreRun(ctx, opLabel{}, md.ImageID, nil, "version"); !errors.Is(err, context.Canceled) {
		t.Fatalf("did not preserve cancellation: %v", err)
	}
	if !runner.removed || fake.container == nil || !fake.container.State.Running {
		t.Fatal("failed validator cleanup did not preserve business container")
	}
}

func TestExplicitValidationUsesOneTemporaryContainer(t *testing.T) {
	m, fake := deployed(t)
	fake.calls = nil
	if err := m.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 || fake.calls[0][0] != "run" {
		t.Fatalf("explicit validation called Docker unnecessarily: %v", fake.calls)
	}
}

func TestCredentialRotationDoesNotProbeUnchangedTarget(t *testing.T) {
	m, _ := deployed(t)
	called := false
	m.targetCheck = func(context.Context, string, string, string) error {
		called = true
		return errors.New("target unavailable")
	}
	if err := m.Reset(context.Background(), model.ResetOptions{Credentials: true}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("credential rotation probed unchanged target")
	}
	if err := m.Reset(context.Background(), model.ResetOptions{SNI: "new.example.com"}); err == nil || !called {
		t.Fatal("SNI change skipped target validation")
	}
}

func TestConfigChangeStoresOnlyCanonicalState(t *testing.T) {
	m, _ := deployed(t)
	var writes []string
	m.stateWrite = func(path string, data []byte, mode os.FileMode) error {
		writes = append(writes, filepath.Base(path))
		return atomicWrite(path, data, mode)
	}
	if err := m.PatchSetting(context.Background(), "log", "info"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writes, []string{"state.json"}) {
		t.Fatalf("unrelated sidecar writes: %v", writes)
	}
	for _, name := range []string{"access.json", "landings.json", "relays.json", "preferences.json"} {
		if _, err := os.Stat(filepath.Join(m.paths.Data(), name)); !os.IsNotExist(err) {
			t.Fatalf("obsolete sidecar created: %s %v", name, err)
		}
	}
}

type fixedSNIResolver struct {
	ips     []net.IPAddr
	lookups []string
}

func (r *fixedSNIResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.lookups = append(r.lookups, host)
	return r.ips, nil
}

func (*fixedSNIResolver) LookupCNAME(context.Context, string) (string, error) {
	return "", errors.New("unexpected CNAME lookup")
}

func TestSNIProbeChecksSharedServerAndLocalAddresses(t *testing.T) {
	for _, kind := range []string{"private", "server", "local"} {
		t.Run(kind, func(t *testing.T) {
			ip := net.ParseIP("1.1.1.1")
			var server []net.IPAddr
			var local []net.IP
			switch kind {
			case "private":
				ip = net.ParseIP("192.168.1.1")
			case "server":
				server = []net.IPAddr{{IP: ip}}
			case "local":
				local = []net.IP{ip}
			}
			resolver := &fixedSNIResolver{ips: []net.IPAddr{{IP: ip}}}
			if _, err := probeOneSNI(context.Background(), "example.com", resolver, server, local); err == nil {
				t.Fatal("SNI probe accepted a forbidden target")
			}
			if !reflect.DeepEqual(resolver.lookups, []string{"example.com"}) {
				t.Fatalf("SNI probe repeated shared lookups: %v", resolver.lookups)
			}
		})
	}
}

func TestDockerCommandTraceRequiresDebugFlag(t *testing.T) {
	for _, debug := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "debug"}[debug], func(t *testing.T) {
			m, fake := deployed(t)
			var output bytes.Buffer
			cmd := m.Command()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			args := []string{"config", "set", "log", "info"}
			if debug {
				args = append([]string{"--debug"}, args...)
			}
			fake.calls = nil
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			text := output.String()
			if len(fake.calls) == 0 || !strings.Contains(text, "校验结果") || !strings.Contains(text, "通过") || !strings.Contains(text, "配置已生效") {
				t.Fatalf("operation or progress suppressed: %s", text)
			}
			if strings.Contains(text, "调用：") || strings.Contains(text, "命令详情") != debug || strings.Contains(text, "docker --host "+endpoint) != debug {
				t.Fatalf("debug=%v trace output: %s", debug, text)
			}
			if !debug && !strings.Contains(text, "docker compose … restart xray") {
				t.Fatalf("missing short command: %s", text)
			}
		})
	}
}

type restartedHealthRunner struct {
	*simulatedDocker
	inspections int
}

func (r *restartedHealthRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := args[2:]
	if call[0] == "container" && call[1] == "inspect" {
		r.inspections++
		if r.inspections == 2 {
			r.container.RestartCount++
		}
	}
	return r.simulatedDocker.Run(ctx, name, args...)
}

func TestSlowerHealthPollingStillDetectsRestart(t *testing.T) {
	m, fake := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	m.runner = &restartedHealthRunner{simulatedDocker: fake}
	ctx, cancel := context.WithTimeout(context.Background(), 3*healthPollInterval)
	defer cancel()
	if err := m.waitHealthy(ctx, nil, md.ImageID); err == nil || !strings.Contains(err.Error(), "自动重启") {
		t.Fatalf("missed restart with slower polling: %v", err)
	}
}

func TestHealthCheckHonorsCancellationBeforeQuerying(t *testing.T) {
	m, fake := deployed(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake.calls = nil
	if err := m.waitHealthy(ctx, nil, fake.container.Image); !errors.Is(err, context.Canceled) || len(fake.calls) != 0 {
		t.Fatalf("canceled health check queried Docker: %v %v", err, fake.calls)
	}
}
