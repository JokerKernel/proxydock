package docker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProgramMenuRoutesConfirmedActionsAndExits(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{input: "7\n1\n", want: "update"},
		{input: "8\n1\n", want: "uninstall"},
		{input: "7\n0\nq\n"},
		{input: "8\n0\nq\n"},
	} {
		m, fake := testManager(t)
		var output bytes.Buffer
		m.out, m.errOut = &output, &output
		got := ""
		m.programManagement = func(_ context.Context, action string) error { got = action; return nil }
		if err := m.Menu(context.Background(), strings.NewReader(test.input)); err != nil {
			t.Fatal(err)
		}
		if got != test.want || fake.container != nil {
			t.Fatalf("input=%q action=%q want=%q container=%v", test.input, got, test.want, fake.container)
		}
		if !strings.Contains(output.String(), "7   更新 ProxyDock") || !strings.Contains(output.String(), "8   卸载 ProxyDock") {
			t.Fatal("standalone program controls missing")
		}
	}
}

func TestProgramMenuReportsFailureAndReturnsToMenu(t *testing.T) {
	m, _ := testManager(t)
	var output bytes.Buffer
	m.out, m.errOut = &output, &output
	m.programManagement = func(context.Context, string) error { return errors.New("installer failed") }
	if err := m.Menu(context.Background(), strings.NewReader("7\n1\nq\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "installer failed") || strings.Contains(output.String(), "更新流程已完成") {
		t.Fatalf("failure was not reported: %s", output.String())
	}
}
