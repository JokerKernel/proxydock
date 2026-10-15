package docker

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
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
	md, err := m.metadata()
	if err != nil || !md.RunAsRoot {
		t.Fatalf("run_as_root = %t, err = %v", md.RunAsRoot, err)
	}
	if f.container != nil {
		t.Fatal("prepare started a container")
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
		t.Fatal(err)
	}
	b := readFile(t, m.paths.Compose())
	if !bytes.Contains(b, []byte("user: \"0:0\"")) || f.container == nil || f.container.Config.User != "0:0" {
		t.Fatalf("first config did not use prepared root selection:\n%s", b)
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

func TestPrepareRecordsDistrolessUser(t *testing.T) {
	m, f := testManager(t)
	f.imageUser = "65532"
	if err := m.Prepare(context.Background(), "26.3.27"); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageUser != "65532:65532" || md.RunAsRoot {
		t.Fatalf("image user %q root %t", md.ImageUser, md.RunAsRoot)
	}
	b, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("user:")) || !bytes.Contains(b, []byte("uid=65532,gid=65532")) {
		t.Fatalf("compose = %s", b)
	}
}

func TestImageUserContainerRemainsManaged(t *testing.T) {
	m, f := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.ImageUser = "65532:65532"
	md.RunAsRoot = false
	compose, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Compose(), compose, 0600); err != nil {
		t.Fatal(err)
	}
	md.ComposeSHA256 = digest(compose)
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	f.imageUser = "65532"
	f.imageUsers[md.ImageID] = "65532"
	f.newContainer(md.ImageID)
	f.container.State.Running = true
	f.container.State.Status = "running"
	if f.container.Config.User != "65532" {
		t.Fatalf("copied user %q", f.container.Config.User)
	}
	for _, user := range []string{"65532", "65532:65532", "1000:1000"} {
		f.container.Config.User = user
		st, err := m.Status(context.Background())
		if err != nil || !st.Running {
			t.Fatalf("user %s rejected: %+v %v", user, st, err)
		}
	}
}

func TestContainerUserDefinesImageUser(t *testing.T) {
	m, f := deployed(t)
	f.container.Config.User = "65532"
	md, _, err := m.prepareOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageUser != "65532:65532" {
		t.Fatalf("defined user %q", md.ImageUser)
	}
	b := readFile(t, m.paths.Compose())
	if bytes.Contains(b, []byte("user:")) || !bytes.Contains(b, []byte("uid=65532,gid=65532")) {
		t.Fatalf("compose = %s", b)
	}
	f.container.Config.User = "1000:1000"
	md, _, err = m.prepareOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageUser != "1000:1000" || !bytes.Contains(readFile(t, m.paths.Compose()), []byte("uid=1000,gid=1000")) {
		t.Fatalf("user %q compose %s", md.ImageUser, readFile(t, m.paths.Compose()))
	}
	rootCompose, err := m.composeFor(Metadata{ImageUser: md.ImageUser, RunAsRoot: true, Network: md.Network, ImageRef: md.ImageRef}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Compose(), rootCompose, 0600); err != nil {
		t.Fatal(err)
	}
	md.RunAsRoot = true
	md.ComposeSHA256 = digest(rootCompose)
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	f.container.Config.User = "65532"
	got, _, err := m.prepareOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ImageUser != "1000:1000" || !bytes.Contains(readFile(t, m.paths.Compose()), []byte("user: \"0:0\"")) {
		t.Fatalf("root selection adopted the container user %q\n%s", got.ImageUser, readFile(t, m.paths.Compose()))
	}
	f.container.Config.User = "nonroot"
	md.RunAsRoot = false
	plain, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = atomicWrite(m.paths.Compose(), plain, 0600); err != nil {
		t.Fatal(err)
	}
	md.ComposeSHA256 = digest(plain)
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	if _, _, err = m.prepareOperation(context.Background()); err == nil || !strings.Contains(err.Error(), "容器用户") {
		t.Fatalf("unparsed container user: %v", err)
	}
}

func TestRecoveryRollsBackImageUserContainer(t *testing.T) {
	m, f := deployed(t)
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	md.ImageUser = "65532:65532"
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		t.Fatal(err)
	}
	f.container.Config.User = "65532"
	if _, err = m.begin("config", nil); err != nil {
		t.Fatal(err)
	}
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.container != nil {
		t.Fatal("container created during the unfinished config change was kept")
	}
	if _, err = os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatal(err)
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
