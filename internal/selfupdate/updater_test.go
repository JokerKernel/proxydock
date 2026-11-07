package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxydock/internal/terminal"
)

type runnerStub struct {
	command, path, body, version string
	args                         []string
	input                        io.Reader
	stdout, stderr               io.Writer
	err                          error
}

func (r *runnerStub) Run(_ context.Context, input io.Reader, stdout, stderr io.Writer, command string, args ...string) error {
	if command == "env" {
		r.version = strings.TrimPrefix(args[0], "PROXYDOCK_VERSION=")
		args = args[2:]
	}
	r.command, r.path, r.args, r.input = command, args[0], append([]string(nil), args...), input
	r.stdout, r.stderr = stdout, stderr
	content, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	info, err := os.Stat(r.path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0700 {
		return errors.New("temporary script must be private")
	}
	r.body = string(content)
	_, _ = io.WriteString(stdout, "installer output\n")
	return r.err
}

func TestInstallerReceivesOriginalOutputWriters(t *testing.T) {
	runner := &runnerStub{}
	var stdout, stderr bytes.Buffer
	u := testUpdater(runner)
	u.Stdout, u.Stderr = terminal.NewColorWriter(&stdout, true), terminal.NewColorWriter(&stderr, true)
	if err := u.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	if runner.stdout != &stdout || runner.stderr != &stderr {
		t.Fatal("installer output is still wrapped, preventing terminal detection")
	}
}

func TestRunDownloadedInstallerWithSeparateArgumentsAndAttachedOutput(t *testing.T) {
	t.Setenv("PROXYDOCK_VERSION", "parent-version")
	runner := &runnerStub{}
	var output bytes.Buffer
	input := strings.NewReader("yes\n")
	u := testUpdater(runner)
	u.Stdin, u.Stdout = input, &output
	requests := 0
	u.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.String() != defaultScriptURL {
			t.Fatalf("unexpected script source: %s", req.URL)
		}
		return scriptResponse(req, testScript), nil
	})
	if err := u.Run(context.Background(), Options{Version: "v1.2.3", AssumeYes: true}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || runner.command != "env" || runner.body != testScript || runner.input != input {
		t.Fatal("updater did not use the downloaded installer and attached input")
	}
	if runner.version != "v1.2.3" {
		t.Fatalf("PROXYDOCK_VERSION=%q", runner.version)
	}
	if os.Getenv("PROXYDOCK_VERSION") != "parent-version" {
		t.Fatal("updater modified the parent environment")
	}
	want := []string{"--yes"}
	if !reflect.DeepEqual(runner.args[1:], want) {
		t.Fatalf("arguments=%v, want %v", runner.args[1:], want)
	}
	for _, expected := range []string{"准备程序管理脚本", defaultScriptURL, "重定向后地址", "脚本大小", "脚本 SHA-256", "bash -n，校验通过", "启动程序管理", "installer output\n"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output missing %q: %s", expected, output.String())
		}
	}
	if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
		t.Fatal("temporary installer was not removed")
	}
}

func TestUninstallDownloadsScriptAndPassesArguments(t *testing.T) {
	for _, yes := range []bool{false, true} {
		runner := &runnerStub{}
		if err := testUpdater(runner).Run(context.Background(), Options{Uninstall: true, AssumeYes: yes}); err != nil {
			t.Fatal(err)
		}
		want := []string{"uninstall"}
		if yes {
			want = append(want, "--yes")
		}
		if !reflect.DeepEqual(runner.args[1:], want) {
			t.Fatalf("arguments=%v, want %v", runner.args[1:], want)
		}
	}
}

func TestFailureReportedAndTemporaryScriptRemoved(t *testing.T) {
	want := errors.New("installer failed")
	runner := &runnerStub{err: want}
	err := testUpdater(runner).Run(context.Background(), Options{})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "执行 ProxyDock 安装/更新脚本") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
		t.Fatal("temporary installer remained after failure")
	}
}

const testScript = "#!/usr/bin/env bash\nprintf 'updated\\n'\n"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func scriptResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: req,
	}
}

func testUpdater(runner Runner) Updater {
	return Updater{
		Runner: runner,
		Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return scriptResponse(req, testScript), nil
		})},
	}
}

func TestCancelledContextDoesNotStartInstaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &runnerStub{}
	if err := (Updater{Runner: runner}).Run(ctx, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if runner.path != "" {
		t.Fatal("installer started after cancellation")
	}
}

func TestExecRunnerCancelsChildrenAndAllowsShellCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	err := (ExecRunner{}).Run(ctx, nil, &output, &output, "bash", "-c", "trap 'printf cleaned; exit 143' TERM; printf ready; sleep 30")
	if err == nil || !strings.Contains(output.String(), "ready") || !strings.Contains(output.String(), "cleaned") {
		t.Fatalf("cancellation did not run shell cleanup: error=%v output=%q", err, output.String())
	}
}
