package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"proxydock/scripts"
)

type runnerStub struct {
	command, path, body, version string
	args                         []string
	input                        io.Reader
	err                          error
}

func (r *runnerStub) Run(_ context.Context, input io.Reader, stdout, _ io.Writer, command string, args ...string) error {
	r.command, r.path, r.args, r.input = command, args[0], append([]string(nil), args...), input
	r.version = os.Getenv("PROXYDOCK_VERSION")
	content, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	info, err := os.Stat(r.path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0600 {
		return errors.New("temporary script must be private")
	}
	r.body = string(content)
	_, _ = io.WriteString(stdout, "installer output\n")
	return r.err
}

func TestRunEmbeddedInstallerWithSeparateArgumentsAndAttachedOutput(t *testing.T) {
	runner := &runnerStub{}
	var output bytes.Buffer
	input := strings.NewReader("yes\n")
	u := Updater{Runner: runner, Stdin: input, Stdout: &output}
	if err := u.Run(context.Background(), Options{Version: "v1.2.3", AssumeYes: true}); err != nil {
		t.Fatal(err)
	}
	if runner.command != "bash" || runner.body != scripts.Installer || runner.input != input {
		t.Fatal("updater did not use the embedded installer and attached input")
	}
	if runner.version != "v1.2.3" {
		t.Fatalf("PROXYDOCK_VERSION=%q", runner.version)
	}
	want := []string{"--yes"}
	if !reflect.DeepEqual(runner.args[1:], want) {
		t.Fatalf("arguments=%v, want %v", runner.args[1:], want)
	}
	if output.String() != "installer output\n" {
		t.Fatalf("output=%q", output.String())
	}
	if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
		t.Fatal("temporary installer was not removed")
	}
}

func TestUninstallRetainsConfirmationUnlessExplicitlySkipped(t *testing.T) {
	for _, yes := range []bool{false, true} {
		runner := &runnerStub{}
		if err := (Updater{Runner: runner}).Run(context.Background(), Options{Uninstall: true, AssumeYes: yes}); err != nil {
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
	err := (Updater{Runner: runner}).Run(context.Background(), Options{})
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "update") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(runner.path); !os.IsNotExist(err) {
		t.Fatal("temporary installer remained after failure")
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
