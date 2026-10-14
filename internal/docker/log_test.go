package docker

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestBriefDockerCommandKeepsActionAndDropsBoilerplate(t *testing.T) {
	compose := []string{"--host", endpoint, "compose", "--project-name", Project, "--project-directory", "/opt/proxyforge/xray-docker", "--file", "/opt/proxyforge/xray-docker/compose.yaml"}
	cases := []struct {
		want string
		args []string
	}{
		{"docker compose … restart xray", append(append([]string{}, compose...), "restart", "xray")},
		{"docker compose … up -d --no-deps --pull never xray", append(append([]string{}, compose...), "up", "-d", "--no-deps", "--pull", "never", "xray")},
		{"docker compose … config --quiet", append(append([]string{}, compose...), "config", "--quiet")},
		{"docker compose … down", append(append([]string{}, compose...), "down")},
		{"docker compose version --short", []string{"--host", endpoint, "compose", "version", "--short"}},
		{"docker image pull ghcr.io/xtls/xray-core:latest", []string{"--host", endpoint, "image", "pull", "ghcr.io/xtls/xray-core:latest"}},
		{"docker run … run -test -config …", []string{"--host", endpoint, "run", "--rm", "--pull", "never", "--name", "check", "--mount", "type=bind,src=/tmp/c.json,dst=/run/proxyforge/config.json,readonly", "sha256:abc", "run", "-test", "-config", "/run/proxyforge/config.json"}},
		{"docker run … version", []string{"--host", endpoint, "run", "--rm", "sha256:abc", "version"}},
		{"docker run … x25519", []string{"--host", endpoint, "run", "--rm", "sha256:abc", "x25519"}},
		{"docker container ls …", []string{"--host", endpoint, "container", "ls", "--all", "--format", "{{.ID}}"}},
		{"docker container stop abc", []string{"--host", endpoint, "container", "stop", "abc"}},
		{"docker container rm … abc", []string{"--host", endpoint, "container", "rm", "--force", "--volumes", "abc"}},
		{"docker logs …", []string{"--host", endpoint, "logs", "--tail", "100", "abc"}},
	}
	for _, tc := range cases {
		if got := briefDockerCommand(tc.args); got != tc.want {
			t.Fatalf("brief(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestHealthQueriesStayQuietUnlessDebug(t *testing.T) {
	m, _ := deployed(t)
	var errOut bytes.Buffer
	m.errOut = &errOut
	if _, err := m.inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("default inspect logged: %s", errOut.String())
	}
	m.traceDocker = true
	if _, err := m.inspect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "命令详情") || !strings.Contains(errOut.String(), "container inspect") {
		t.Fatalf("debug inspect: %s", errOut.String())
	}
}

func TestDebugLogsDoNotIncludePrivateKeys(t *testing.T) {
	m, _ := deployed(t)
	m.traceDocker = true
	var errOut bytes.Buffer
	m.errOut = &errOut
	before, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Reset(context.Background(), model.ResetOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	text := errOut.String()
	if strings.Contains(text, before.Node.PrivateKey) || strings.Contains(text, after.Node.PrivateKey) || strings.Contains(text, "PrivateKey:") {
		t.Fatal("debug log leaked a private key")
	}
	if !strings.Contains(text, "命令详情") || !strings.Contains(text, "x25519") {
		t.Fatalf("debug did not record the key command: %s", text)
	}
}
