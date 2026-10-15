package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"proxydock/internal/docker"
	"proxydock/internal/selfupdate"
)

func TestProgramManagementOptionsWithoutDocker(t *testing.T) {
	for _, test := range []struct {
		args []string
		want selfupdate.Options
	}{
		{args: []string{"update"}},
		{args: []string{"update", "--version", "v1.2.3", "--yes"}, want: selfupdate.Options{Version: "v1.2.3", AssumeYes: true}},
		{args: []string{"uninstall"}, want: selfupdate.Options{Uninstall: true}},
		{args: []string{"--yes", "uninstall"}, want: selfupdate.Options{Uninstall: true, AssumeYes: true}},
		{args: []string{"--debug", "update", "--yes"}, want: selfupdate.Options{AssumeYes: true}},
	} {
		var got selfupdate.Options
		called := false
		root := newCommandWithUpdater("test", docker.Options{
			RootCheck: func() error { return nil },
			LookPath:  func(string) (string, error) { t.Fatal("program management inspected Docker"); return "", nil },
		}, func(_ context.Context, options selfupdate.Options) error {
			called, got = true, options
			return nil
		})
		root.SetArgs(test.args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !called || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%v: called=%v options=%+v, want %+v", test.args, called, got, test.want)
		}
	}
}

func TestManagementFailureIsReturned(t *testing.T) {
	want := errors.New("installer failed")
	root := newCommandWithUpdater("test", docker.Options{RootCheck: func() error { return nil }}, func(context.Context, selfupdate.Options) error { return want })
	root.SetArgs([]string{"update"})
	if err := root.Execute(); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}
