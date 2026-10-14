package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"proxydock/internal/docker"
)

func TestStandaloneCommandTree(t *testing.T) {
	root := New("test")
	for _, args := range [][]string{{"prepare"}, {"deploy"}, {"upgrade"}, {"status"}, {"recover"}, {"remove"}, {"cleanup"}, {"config", "generate"}, {"config", "client"}, {"config", "access"}, {"config", "landing"}, {"config", "relay"}, {"service"}} {
		cmd, remaining, err := root.Find(args)
		if err != nil || len(remaining) != 0 || cmd.Name() != args[len(args)-1] {
			t.Fatalf("find %v: command=%v remaining=%v error=%v", args, cmd, remaining, err)
		}
	}
	for _, removed := range []string{"docker", "install", "update", "uninstall"} {
		if _, remaining, err := root.Find([]string{removed}); err == nil && len(remaining) == 0 {
			t.Fatalf("unexpected inherited command %q", removed)
		}
	}
}

func TestHelpAndVersionDoNotRequireRootOrDocker(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"deploy", "--help"}, {"config", "--help"}, {"--version"}, {"-v"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			root := newCommand("v1.2.3\ncommit: source", docker.Options{
				Out: &out, ErrOut: &out,
				RootCheck: func() error { t.Fatal("root check during help/version"); return nil },
				LookPath:  func(string) (string, error) { t.Fatal("Docker check during help/version"); return "", nil },
			})
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if args[0] == "--version" || args[0] == "-v" {
				if got, want := out.String(), "proxydock v1.2.3\ncommit: source\n"; got != want {
					t.Fatalf("version = %q, want %q", got, want)
				}
			} else if !strings.Contains(out.String(), "proxydock") || strings.Contains(out.String(), "proxyforge") {
				t.Fatalf("standalone help has wrong identity: %s", out.String())
			}
		})
	}
}

func TestOperationsRequireRootBeforeCheckingDocker(t *testing.T) {
	want := errors.New("root required")
	for _, args := range [][]string{nil, {"status"}, {"deploy"}, {"service", "start"}, {"config", "client"}, {"cleanup", "--yes"}} {
		root := newCommand("test", docker.Options{
			RootCheck: func() error { return want },
			LookPath:  func(string) (string, error) { t.Fatal("Docker check before root"); return "", nil },
		})
		root.SetArgs(args)
		if err := root.Execute(); !errors.Is(err, want) {
			t.Fatalf("%v: error=%v, want %v", args, err, want)
		}
	}
}
