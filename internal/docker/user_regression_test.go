package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestUpgradePreservesOrOverridesRunUser(t *testing.T) {
	for _, tc := range []struct {
		name, version, user   string
		initialRoot, wantRoot bool
	}{
		{"new-image-keeps-root", "latest", "", true, true},
		{"same-image-keeps-root", "26.3.27", "", true, true},
		{"new-image-keeps-default", "latest", "", false, false},
		{"new-image-selects-root", "latest", "root", false, true},
		{"new-image-selects-default", "latest", "default", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m, f := deployed(t)
			if tc.initialRoot {
				if err := m.SetRunAsRoot(ctx, true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.version == "latest" {
				f.imageUser = "65532:65532"
			}
			f.calls = nil
			if err := m.upgrade(ctx, tc.version, "", tc.user); err != nil {
				t.Fatal(err)
			}
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			pinned := bytes.Contains(readFile(t, m.paths.Compose()), []byte(`user: "0:0"`))
			if md.RunAsRoot != tc.wantRoot || pinned != tc.wantRoot {
				t.Fatalf("root selection changed: metadata=%t compose=%t want=%t", md.RunAsRoot, pinned, tc.wantRoot)
			}
			if tc.wantRoot && f.container.Config.User != "0:0" {
				t.Fatalf("container user = %q", f.container.Config.User)
			}
			for _, call := range f.calls {
				if call[0] != "run" || !contains(call, "-test") {
					continue
				}
				if contains(call, "--user") != tc.wantRoot {
					t.Fatalf("validator did not use selected user: %v", call)
				}
				if !tc.wantRoot && !strings.Contains(strings.Join(call, " "), "uid=65532,gid=65532") {
					t.Fatalf("validator did not use target uid: %v", call)
				}
			}
		})
	}
}

func TestFailedCandidateValidationLeavesLiveFilesUntouched(t *testing.T) {
	m, f := deployed(t)
	certDir := filepath.Join(m.paths.TLS(), "existing")
	if err := privateDir(certDir); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(certDir, "key.pem")
	if err := atomicWrite(key, []byte("existing private key"), 0600); err != nil {
		t.Fatal(err)
	}
	oldMetadata := readFile(t, m.paths.Metadata())
	oldConfig := readFile(t, m.paths.Config())
	oldImage := f.container.Image
	f.calls = nil
	f.imageUser = "65532:65532"
	f.rejectValidation = true
	if err := m.Upgrade(context.Background(), "latest"); err == nil {
		t.Fatal("validation unexpectedly succeeded")
	}
	if !bytes.Equal(oldMetadata, readFile(t, m.paths.Metadata())) || !bytes.Equal(oldConfig, readFile(t, m.paths.Config())) || f.container.Image != oldImage || !f.container.State.Running {
		t.Fatal("failed preflight changed the deployment")
	}
	if string(readFile(t, key)) != "existing private key" {
		t.Fatal("live certificate changed")
	}
	for path := range f.fileOwners {
		if strings.HasPrefix(path, m.paths.TLS()) || path == m.paths.Config() {
			t.Fatalf("preflight changed live ownership: %s", path)
		}
	}
	checked := false
	for _, call := range f.calls {
		if call[0] != "run" || !contains(call, "-test") {
			continue
		}
		checked = true
		for i, arg := range call {
			if arg != "--mount" {
				continue
			}
			var source string
			for _, field := range strings.Split(call[i+1], ",") {
				if strings.HasPrefix(field, "src=") {
					source = strings.TrimPrefix(field, "src=")
				}
			}
			if !strings.HasPrefix(source, filepath.Join(m.paths.Staging(), "validation-")) {
				t.Fatalf("validator mounted live data: %s", source)
			}
			if owner := f.fileOwners[source]; owner.uid != 65532 || owner.gid != 65532 {
				t.Fatalf("candidate mount owner = %+v", owner)
			}
		}
	}
	if !checked {
		t.Fatal("did not attempt candidate validation")
	}
	entries, err := os.ReadDir(m.paths.Staging())
	if err != nil || len(entries) != 0 {
		t.Fatalf("candidate files leaked: %v %v", entries, err)
	}
	if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatalf("preflight started a transaction: %v", err)
	}
}

func TestUpgradeRollbackRestoresPreviousImageUser(t *testing.T) {
	ctx := context.Background()
	m, f := testManager(t)
	f.imageUser = "1000:1000"
	if err := m.Prepare(ctx, "26.3.27"); err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(ctx, model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true}, true); err != nil {
		t.Fatal(err)
	}
	previous := readFile(t, m.paths.Metadata())
	f.imageUser = "65532:65532"
	f.failComposeUp = 1
	if err := m.Upgrade(ctx, "latest"); err == nil {
		t.Fatal("recreate failure unexpectedly succeeded")
	}
	if !bytes.Equal(previous, readFile(t, m.paths.Metadata())) || f.container.Config.User != "1000:1000" || !f.container.State.Running {
		t.Fatal("rollback did not restore the previous image and user")
	}
	if owner := f.fileOwners[m.paths.Config()]; owner.uid != 1000 || owner.gid != 1000 {
		t.Fatalf("rollback config owner = %+v", owner)
	}
	if _, err := os.Stat(m.paths.Journal()); !os.IsNotExist(err) {
		t.Fatalf("rollback left a journal: %v", err)
	}
}

func TestRootSwitchDoesNotRequireResolvingExistingUser(t *testing.T) {
	m, f := deployed(t)
	f.container.Config.User = "unknown-user"
	f.calls = nil
	if err := m.SetRunAsRoot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if f.container.Config.User != "0:0" {
		t.Fatalf("root recovery user = %q", f.container.Config.User)
	}
	for _, call := range f.calls {
		if call[0] == "create" || call[0] == "cp" {
			t.Fatalf("root recovery tried resolving old user: %v", call)
		}
	}
}

func TestRootSwitchStillRejectsForeignContainer(t *testing.T) {
	m, f := deployed(t)
	f.container.Config.User = "unknown-user"
	f.container.Config.Labels["io.proxydock.managed"] = "false"
	if err := m.SetRunAsRoot(context.Background(), true); err == nil {
		t.Fatal("root recovery adopted a foreign container")
	}
}

type imageAccountsRunner struct {
	*simulatedDocker
	files    map[string]string
	name     string
	removed  bool
	failCopy bool
	cancel   context.CancelFunc
}

func (r *imageAccountsRunner) Run(ctx context.Context, executable string, args ...string) ([]byte, error) {
	a := args[2:]
	switch a[0] {
	case "create":
		for i, arg := range a {
			if arg == "--name" {
				r.name = a[i+1]
			}
		}
		return []byte("account-reader-id\n"), nil
	case "cp":
		if r.failCopy {
			if r.cancel != nil {
				r.cancel()
			}
			return nil, context.Canceled
		}
		_, path, _ := strings.Cut(a[1], ":")
		data, ok := r.files[path]
		if !ok {
			return nil, errors.New("account file unavailable")
		}
		var archive bytes.Buffer
		writer := tar.NewWriter(&archive)
		if err := writer.WriteHeader(&tar.Header{Name: filepath.Base(path), Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := writer.Write([]byte(data)); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return archive.Bytes(), nil
	case "container":
		if a[1] == "inspect" && a[2] == r.name {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			in := inspection{ID: "account-reader-id", Name: "/" + r.name}
			in.Config.Labels = map[string]string{"io.proxydock.managed": "true", "io.proxydock.instance": Instance, "io.proxydock.role": "validator"}
			return json.Marshal([]inspection{in})
		}
		if a[1] == "rm" && a[len(a)-1] == "account-reader-id" {
			r.removed = true
			return nil, nil
		}
	}
	return r.simulatedDocker.Run(ctx, executable, args...)
}

func TestResolveNamedImageUsersAndGroups(t *testing.T) {
	for _, tc := range []struct{ spec, want string }{
		{"nonroot", "65532:1234"},
		{"nonroot:service", "65532:4321"},
		{"nonroot:2000", "65532:2000"},
		{"1000:service", "1000:4321"},
		{"missing", ""},
		{"nonroot:missing", ""},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			m, f := prepared(t)
			runner := &imageAccountsRunner{simulatedDocker: f, files: map[string]string{
				"/etc/passwd": "root:x:0:0:root:/root:/bin/sh\nnonroot:x:65532:1234:service:/:/sbin/nologin\n",
				"/etc/group":  "service:x:4321:\n",
			}}
			m.runner = runner
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			actual, err := m.resolveImageUser(context.Background(), md.ImageID, tc.spec)
			if actual != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("resolved %q: %q %v", tc.spec, actual, err)
			}
			if !runner.removed {
				t.Fatal("temporary account reader leaked")
			}
		})
	}
}

func TestNamedImagePreparationAndContainerUser(t *testing.T) {
	m, f := testManager(t)
	f.imageUser = "nonroot"
	runner := &imageAccountsRunner{simulatedDocker: f, files: map[string]string{"/etc/passwd": "nonroot:x:65532:1234:service:/:/sbin/nologin\n"}}
	m.runner = runner
	if err := m.prepareWithNetwork(context.Background(), "26.3.27", networkHost, false); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil || md.ImageUser != "65532:1234" {
		t.Fatalf("prepared user: %q %v", md.ImageUser, err)
	}
	f.newContainer(md.ImageID)
	f.container.State.Running = true
	if _, _, err := m.prepareOperation(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNamedUserReaderCleansUpAfterCopyFailure(t *testing.T) {
	m, f := prepared(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &imageAccountsRunner{simulatedDocker: f, failCopy: true, cancel: cancel}
	m.runner = runner
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.resolveImageUser(ctx, md.ImageID, "nonroot"); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy error lost: %v", err)
	}
	if !runner.removed {
		t.Fatal("failed account reader leaked")
	}
}

func TestCopyValidationTLSRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "copy")
	if err := privateDir(source); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := atomicWrite(external, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := copyValidationTLS(source, destination); err == nil {
		t.Fatal("copied certificate through symlink")
	}
}

func TestCleanupValidationDirectoriesRequiresMarker(t *testing.T) {
	m, _ := prepared(t)
	managed := filepath.Join(m.paths.Staging(), "validation-123")
	unmanaged := filepath.Join(m.paths.Staging(), "validation-456")
	for _, dir := range []string{managed, unmanaged} {
		if err := privateDir(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveJSON(filepath.Join(managed, ".proxydock-validation.json"), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanupValidationDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatalf("managed validation snapshot remains: %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unmarked directory removed: %v", err)
	}
}
