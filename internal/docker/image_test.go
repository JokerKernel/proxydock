package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestLatestPreparePinsDigestAndRecordsActualVersion(t *testing.T) {
	m, fake := testManager(t)
	if err := m.Prepare(context.Background(), " latest "); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Channel != "latest" || md.RequestedVersion != "26.4.1" || !strings.HasPrefix(md.Version, "Xray 26.4.1 ") {
		t.Fatalf("latest lost its channel or actual version: %+v", md)
	}
	compose, err := m.composeFor(md, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(compose, []byte(md.ImageRef)) || bytes.Contains(compose, []byte(Repository+":latest")) {
		t.Fatalf("latest deployment must pin digest: %s", compose)
	}
	if fake.container != nil {
		t.Fatal("prepare started a business container")
	}
	pulled := false
	for _, call := range fake.calls {
		if len(call) == 3 && call[0] == "image" && call[1] == "pull" {
			pulled = true
			if call[2] != Repository+":latest" {
				t.Fatalf("wrong pull: %v", call)
			}
		}
	}
	if !pulled {
		t.Fatal("latest tag was not pulled")
	}
}

func TestLatestDeploymentEntrypoints(t *testing.T) {
	for _, entry := range []string{"manager", "command"} {
		for _, alreadyPrepared := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/prepared=%t", entry, alreadyPrepared), func(t *testing.T) {
				m, fake := testManager(t)
				if alreadyPrepared {
					if err := m.Prepare(context.Background(), "latest"); err != nil {
						t.Fatal(err)
					}
				}
				fake.calls = nil
				var err error
				if entry == "manager" {
					err = m.Deploy(context.Background(), "latest", model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443", StandardConfig: true})
				} else {
					cmd := m.Command()
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					cmd.SetIn(strings.NewReader(""))
					cmd.SetArgs([]string{"deploy", "--version", "latest", "--server", "example.com", "--port", "8443", "--sni", "www.example.com", "--target", "www.example.com:443", "--standard-config"})
					err = cmd.ExecuteContext(context.Background())
				}
				if err != nil {
					t.Fatal(err)
				}
				if fake.container == nil || !fake.container.State.Running {
					t.Fatal("latest deployment did not start")
				}
				if alreadyPrepared {
					for _, call := range fake.calls {
						if contains(call, "pull") {
							t.Fatalf("deploy must reuse prepared digest: %v", call)
						}
					}
				}
			})
		}
	}
}

func TestLatestUpgradeRefreshesDigestAndRollsBack(t *testing.T) {
	m, fake := deployed(t)
	ctx := context.Background()
	if err := m.Upgrade(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	oldMD, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	oldState := readFile(t, m.paths.State())
	oldCompose := readFile(t, m.paths.Compose())
	fake.latestVersion = "26.4.2"
	fake.latestImageID = "sha256:" + strings.Repeat("c", 64)
	fake.healthFailures = 1
	if err := m.Upgrade(ctx, "latest"); err == nil {
		t.Fatal("unhealthy latest image accepted")
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageID != oldMD.ImageID || md.Channel != "latest" || fake.container.Image != oldMD.ImageID || !fake.container.State.Running || !bytes.Equal(oldState, readFile(t, m.paths.State())) || !bytes.Equal(oldCompose, readFile(t, m.paths.Compose())) {
		t.Fatal("failed latest upgrade did not restore deployment")
	}
	if _, err := m.Service(ctx, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := m.Upgrade(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	md, err = m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.ImageID != fake.latestImageID || md.RequestedVersion != "26.4.2" || fake.container.Image != md.ImageID || fake.container.State.Running {
		t.Fatal("latest upgrade did not refresh digest while preserving stopped state")
	}
	fake.calls = nil
	if _, err := m.Service(ctx, "start"); err != nil {
		t.Fatal(err)
	}
	for _, call := range fake.calls {
		if contains(call, "pull") {
			t.Fatalf("start must reuse latest digest: %v", call)
		}
	}
}

func TestUpgradeDefaultsToSavedSelection(t *testing.T) {
	for _, entry := range []string{"manager", "command"} {
		for _, selection := range []string{"latest", "pre-release", "26.3.27"} {
			for _, configured := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/configured=%t", entry, selection, configured), func(t *testing.T) {
					m, fake := testManager(t)
					ctx := context.Background()
					if err := m.Prepare(ctx, selection); err != nil {
						t.Fatal(err)
					}
					if configured {
						if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
							t.Fatal(err)
						}
					}
					old, err := m.metadata()
					if err != nil {
						t.Fatal(err)
					}
					fake.latestVersion, fake.prereleaseVersion = "26.10.11", "26.10.12"
					fake.latestImageID = "sha256:" + strings.Repeat("e", 64)
					fake.prereleaseImageID = "sha256:" + strings.Repeat("f", 64)
					fake.calls = nil
					if entry == "manager" {
						err = m.Upgrade(ctx, "")
					} else {
						cmd := m.Command()
						cmd.SetOut(io.Discard)
						cmd.SetErr(io.Discard)
						cmd.SetArgs([]string{"upgrade"})
						err = cmd.ExecuteContext(ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
					md, err := m.metadata()
					if err != nil {
						t.Fatal(err)
					}
					if md.Channel != old.Channel {
						t.Fatalf("default upgrade changed channel: %s -> %s", old.Channel, md.Channel)
					}
					wantImage := old.ImageID
					if selection == "latest" {
						wantImage = fake.latestImageID
					} else if selection == "pre-release" {
						wantImage = fake.prereleaseImageID
					}
					if md.ImageID != wantImage {
						t.Fatalf("default upgrade did not use saved selection: %+v", md)
					}
					if count := countDockerCalls(fake, func(c []string) bool {
						return len(c) == 3 && c[0] == "image" && c[1] == "pull" && c[2] == Repository+":"+selection
					}); count != 1 {
						t.Fatalf("default upgrade did not pull %q: %v", selection, fake.calls)
					}
				})
			}
		}
	}
}

func TestUpgradeDeletesOnlyReplacedImageAfterSuccess(t *testing.T) {
	for _, state := range []string{"running", "stopped", "removed", "prepared"} {
		t.Run(state, func(t *testing.T) {
			m, f := prepared(t)
			ctx := context.Background()
			if state != "prepared" {
				if err := m.Generate(ctx, model.GenerateOptions{Security: "tls", Server: "example.com", Port: 8443}, true); err != nil {
					t.Fatal(err)
				}
			}
			if state == "stopped" {
				if _, err := m.Service(ctx, "stop"); err != nil {
					t.Fatal(err)
				}
			} else if state == "removed" {
				if err := m.Remove(ctx); err != nil {
					t.Fatal(err)
				}
			}
			old, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			unrelated := "sha256:" + strings.Repeat("e", 64)
			f.images[unrelated] = Metadata{ImageID: unrelated}
			f.calls = nil
			healthChecked := false
			checkHealth := m.healthCheck
			m.healthCheck = func(ctx context.Context, ports []int, imageID string) error {
				for _, call := range f.calls {
					if call[0] == "image" && call[1] == "rm" {
						t.Fatal("old image was removed before health verification")
					}
				}
				if err := checkHealth(ctx, ports, imageID); err != nil {
					return err
				}
				healthChecked = true
				return nil
			}
			if err := m.Upgrade(ctx, "26.4.1"); err != nil {
				t.Fatal(err)
			}
			md, err := m.metadata()
			if err != nil || md.ImageID == old.ImageID {
				t.Fatalf("upgrade not committed: %+v %v", md, err)
			}
			if _, exists := f.images[old.ImageID]; exists {
				t.Fatal("successful upgrade retained the replaced image")
			}
			if _, exists := f.images[md.ImageID]; !exists {
				t.Fatal("successful upgrade deleted its current image")
			}
			if _, exists := f.images[unrelated]; !exists {
				t.Fatal("successful upgrade deleted an unrelated image")
			}
			removals := 0
			for _, call := range f.calls {
				if call[0] == "image" && call[1] == "rm" {
					removals++
					if len(call) != 3 || call[2] != old.ImageID {
						t.Fatalf("unsafe image removal: %v", call)
					}
				}
			}
			if removals != 1 || state == "running" && !healthChecked {
				t.Fatalf("incorrect successful upgrade cleanup: removals=%d health=%t", removals, healthChecked)
			}
		})
	}
}

func TestFailedUpgradeDoesNotDeleteRollbackImage(t *testing.T) {
	for _, failure := range []string{"validation", "recreate", "health", "state-write"} {
		t.Run(failure, func(t *testing.T) {
			m, f := deployed(t)
			old, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "validation":
				f.rejectValidation = true
			case "recreate":
				f.failComposeUp = 1
			case "health":
				f.healthFailures = 1
			case "state-write":
				m.stateWrite = func(string, []byte, os.FileMode) error { return errors.New("state write failed") }
			}
			f.calls = nil
			if err := m.Upgrade(context.Background(), "26.4.1"); err == nil {
				t.Fatal("expected upgrade failure")
			}
			if _, exists := f.images[old.ImageID]; !exists {
				t.Fatal("failed upgrade deleted its rollback image")
			}
			if f.container == nil || !f.container.State.Running || f.container.Image != old.ImageID {
				t.Fatal("failed upgrade did not retain or restore the old container")
			}
			for _, call := range f.calls {
				if call[0] == "image" && call[1] == "rm" {
					t.Fatalf("failed upgrade attempted image deletion: %v", call)
				}
			}
		})
	}
}

func TestImageCleanupFailureDoesNotUndoSuccessfulUpgrade(t *testing.T) {
	m, f := deployed(t)
	old, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	f.failImageRemove = true
	var output bytes.Buffer
	m.errOut = &output
	if err := m.Upgrade(context.Background(), "26.4.1"); err != nil {
		t.Fatalf("image deletion failure undid successful upgrade: %v", err)
	}
	md, err := m.metadata()
	if err != nil || md.ImageID == old.ImageID || f.container.Image != md.ImageID || !f.container.State.Running {
		t.Fatalf("image deletion failure changed the new deployment: %+v %v", md, err)
	}
	if _, exists := f.images[old.ImageID]; !exists {
		t.Fatal("Docker refusal did not preserve the image")
	}
	if !strings.Contains(output.String(), "旧镜像保留") || !strings.Contains(output.String(), "image is in use") {
		t.Fatalf("image deletion failure was not reported: %s", output.String())
	}
}

func TestImageVersionValidation(t *testing.T) {
	for _, tc := range []struct{ selection, output string }{
		{"latest", "Xray unknown (build)\n"},
		{"latest", "invalid output\n"},
		{"26.3.27", "Xray 26.4.1 (build)\n"},
	} {
		t.Run(tc.selection+"/"+tc.output, func(t *testing.T) {
			m, fake := testManager(t)
			fake.versionOutput = tc.output
			if err := m.Prepare(context.Background(), tc.selection); err == nil {
				t.Fatal("invalid/mismatched version accepted")
			}
			if _, err := m.metadata(); err == nil {
				t.Fatal("failed image detection saved deployment metadata")
			}
		})
	}
}

func TestLatestDeployRequiresLatestPreparedChannel(t *testing.T) {
	for _, selection := range []string{"latest", "pre-release"} {
		t.Run(selection, func(t *testing.T) {
			m, fake := prepared(t)
			fake.calls = nil
			if err := m.Deploy(context.Background(), selection, model.GenerateOptions{}); err == nil || !strings.Contains(err.Error(), "升级入口") {
				t.Fatalf("%s deployment reused a pinned version: %v", selection, err)
			}
			if len(fake.calls) != 0 {
				t.Fatal("version selection mismatch mutated deployment")
			}
		})
	}
}

func TestImageSelectionsUseOfficialTags(t *testing.T) {
	for _, tc := range []struct {
		selection, tag, channel, version string
	}{
		{"latest", Repository + ":latest", "latest", "26.4.1"},
		{"", Repository + ":latest", "latest", "26.4.1"},
		{"pre-release", Repository + ":pre-release", "pre-release", "26.10.10"},
	} {
		t.Run(tc.selection+"/"+tc.tag, func(t *testing.T) {
			m, fake := testManager(t)
			if err := m.Prepare(context.Background(), tc.selection); err != nil {
				t.Fatal(err)
			}
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			if md.Channel != tc.channel || md.RequestedVersion != tc.version {
				t.Fatalf("channel or version: %+v", md)
			}
			compose, err := m.composeFor(md, nil)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(compose, []byte(tc.tag)) || !bytes.Contains(compose, []byte(md.ImageRef)) {
				t.Fatalf("compose did not pin digest: %s", compose)
			}
			pulled := false
			for _, call := range fake.calls {
				if len(call) == 3 && call[0] == "image" && call[1] == "pull" {
					pulled = true
					if call[2] != tc.tag {
						t.Fatalf("wrong pull: %v", call)
					}
				}
			}
			if !pulled {
				t.Fatal("official tag was not pulled")
			}
		})
	}
}

func TestPrepareContinuesAfterPullWithoutPrompt(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	m.traceDocker = true
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	pull := strings.Index(text, "image pull ")
	inspect := strings.Index(text, "image inspect ")
	result := strings.Index(text, "准备结果")
	if pull < 0 || inspect < 0 || result < 0 || pull > inspect || inspect > result {
		t.Fatalf("prepare log was split:\n%s", text)
	}
	if in.index != 0 || strings.Contains(text, "按 Enter") {
		t.Fatalf("prepare waited for Enter: index=%d\n%s", in.index, text)
	}
}

func TestPrepareDoesNotPauseWhenPullFails(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, fake := testManager(t)
	fake.failPull = true
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"\n"}, output: &out}
	m.review = newUI(in, &out)
	if err := m.Prepare(context.Background(), "latest"); err == nil {
		t.Fatal("failed pull was accepted")
	}
	if in.index != 0 || strings.Contains(out.String(), "按 Enter") {
		t.Fatalf("failed pull waited for Enter: index=%d\n%s", in.index, out.String())
	}
}
