package docker

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestParseImageUser(t *testing.T) {
	cases := []struct {
		spec   string
		uid    int
		gid    int
		ok     bool
		stored string
	}{
		{spec: "", uid: 0, gid: 0, ok: true, stored: "0:0"},
		{spec: "0:0", uid: 0, gid: 0, ok: true, stored: "0:0"},
		{spec: "root", uid: 0, gid: 0, ok: true, stored: "0:0"},
		{spec: "65532", uid: 65532, gid: 65532, ok: true, stored: "65532:65532"},
		{spec: "65532:65532", uid: 65532, gid: 65532, ok: true, stored: "65532:65532"},
		{spec: "1:2", uid: 1, gid: 2, ok: true, stored: "1:2"},
		{spec: "nonroot", ok: false},
		{spec: "-1", ok: false},
		{spec: "65532:abc", ok: false},
	}
	for _, tc := range cases {
		uid, gid, ok := parseImageUser(tc.spec)
		if uid != tc.uid || gid != tc.gid || ok != tc.ok {
			t.Fatalf("parseImageUser(%q) = %d,%d,%t", tc.spec, uid, gid, ok)
		}
		stored, err := normalizeImageUser(tc.spec)
		if tc.ok {
			if err != nil || stored != tc.stored {
				t.Fatalf("normalizeImageUser(%q) = %q, %v", tc.spec, stored, err)
			}
		} else if err == nil {
			t.Fatalf("normalizeImageUser(%q) accepted a name", tc.spec)
		}
	}
}

func TestComposeUsesImageUserForLogTmpfs(t *testing.T) {
	m, _ := prepared(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.ImageUser = "65532"
	b, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("user:")) {
		t.Fatalf("compose pins a container user:\n%s", b)
	}
	if !bytes.Contains(b, []byte("/var/log/xray:rw,noexec,nosuid,nodev,size=16m,mode=0700,uid=65532,gid=65532")) {
		t.Fatalf("log tmpfs = %s", b)
	}
}

func TestServiceMigratesPinnedRootUser(t *testing.T) {
	m, f := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	previous := md
	previous.ImageUser = "0:0"
	base, err := m.composeFor(previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok := legacyUserCompose(base)
	if !ok || !bytes.Contains(legacy, []byte("user: \"0:0\"")) {
		t.Fatal("could not reconstruct the root pin")
	}
	if err = atomicWrite(m.paths.Compose(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	md.ImageUser = ""
	md.ComposeSHA256 = digest(legacy)
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	f.container.Config.User = "0:0"
	f.calls = nil
	st, err := m.Status(context.Background())
	if err != nil || !st.Running {
		t.Fatalf("legacy root pin rejected: %+v %v", st, err)
	}
	if f.container.Config.User != "0:0" || !bytes.Contains(readFile(t, m.paths.Compose()), []byte(`user: "0:0"`)) {
		t.Fatal("status migrated the container")
	}
	for _, call := range f.calls {
		if contains(call, "up") || contains(call, "restart") {
			t.Fatalf("status recreated the container: %v", call)
		}
	}
	if _, err = m.Service(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	b := readFile(t, m.paths.Compose())
	if bytes.Contains(b, []byte("user:")) {
		t.Fatalf("migrated compose still pins a user:\n%s", b)
	}
	if f.container == nil || f.container.Config.User != "" {
		t.Fatalf("container user = %q", f.container.Config.User)
	}
	md, err = m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageUser != "0:0" || md.ComposeSHA256 != digest(b) {
		t.Fatalf("recorded user %q or compose hash was not updated", md.ImageUser)
	}
}

func TestRunAsRootPinsComposeUser(t *testing.T) {
	m, f := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.ImageUser = "65532:65532"
	md.RunAsRoot = true
	b, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("user: \"0:0\"")) || bytes.Contains(b, []byte("uid=")) {
		t.Fatalf("root compose = %s", b)
	}
	if err = m.SetRunAsRoot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.container == nil || f.container.Config.User != "0:0" {
		t.Fatalf("container user = %q", f.container.Config.User)
	}
	if !bytes.Contains(readFile(t, m.paths.Compose()), []byte("user: \"0:0\"")) {
		t.Fatal("running compose does not pin root")
	}
	f.calls = nil
	if _, err = m.Service(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if f.container.Config.User != "0:0" || !bytes.Contains(readFile(t, m.paths.Compose()), []byte("user: \"0:0\"")) {
		t.Fatal("restart dropped the root option")
	}
	f.calls = nil
	if err = m.SetRunAsRoot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls {
		if contains(call, "up") || contains(call, "restart") {
			t.Fatalf("unchanged root recreated the container: %v", call)
		}
	}
	f.calls = nil
	if err = m.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || !contains(f.calls[0], "--user") || !contains(f.calls[0], "0:0") {
		t.Fatalf("root validation did not run as root: %v", f.calls)
	}
	if err = m.SetRunAsRoot(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(readFile(t, m.paths.Compose()), []byte("user:")) || f.container.Config.User != "" {
		t.Fatalf("default user compose = %s user=%q", readFile(t, m.paths.Compose()), f.container.Config.User)
	}
}

func TestPrepareCanPinRoot(t *testing.T) {
	m, f := testManager(t)
	if err := m.prepareWithNetwork(context.Background(), "26.3.27", networkHost, true); err != nil {
		t.Fatal(err)
	}
	b := readFile(t, m.paths.Compose())
	if !bytes.Contains(b, []byte("user: \"0:0\"")) {
		t.Fatalf("prepare did not pin root:\n%s", b)
	}
	md, err := m.metadata()
	if err != nil || !md.RunAsRoot {
		t.Fatalf("run_as_root = %t, err = %v", md.RunAsRoot, err)
	}
	if f.container != nil {
		t.Fatal("prepare started a container")
	}
}

func TestSetUserCommandSwitchesToRoot(t *testing.T) {
	m, f := deployed(t)
	cmd := m.Command()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"config", "set", "user", "root"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.container == nil || f.container.Config.User != "0:0" {
		t.Fatalf("container user = %q", f.container.Config.User)
	}
	cmd.SetArgs([]string{"config", "set", "user", "default"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.container.Config.User != "" {
		t.Fatalf("container user = %q", f.container.Config.User)
	}
	if _, err := parseRunUser("admin"); err == nil {
		t.Fatal("accepted an unknown run user")
	}
}

func TestChownTreeRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	err := chownTree(t.TempDir(), 65532, 65532)
	if err == nil || !strings.Contains(err.Error(), "需要 root") {
		t.Fatal(err)
	}
}
