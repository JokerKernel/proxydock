package docker

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestHostComposeKeepsHostNetwork(t *testing.T) {
	m, _ := prepared(t)
	b := readFile(t, m.paths.Compose())
	if !strings.Contains(string(b), "network_mode: host") || strings.Contains(string(b), "\n    ports:\n") {
		t.Fatalf("host compose:\n%s", b)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Network != "" {
		t.Fatalf("default network = %q", md.Network)
	}
}

func TestPublishComposeMapsListenAndLoopback(t *testing.T) {
	m, f := testManager(t)
	if err := m.prepareWithNetwork(context.Background(), "26.3.27", networkPublish); err != nil {
		t.Fatal(err)
	}
	preparedCompose := string(readFile(t, m.paths.Compose()))
	if strings.Contains(preparedCompose, "network_mode: host") || strings.Contains(preparedCompose, "\n    ports:\n") {
		t.Fatalf("publish compose before config should not publish ports:\n%s", preparedCompose)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{Server: "example.com", Port: 8443, SNI: "www.example.com", Target: "www.example.com:443"}, true); err != nil {
		t.Fatal(err)
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	compose := string(readFile(t, m.paths.Compose()))
	if strings.Contains(compose, "network_mode: host") || !strings.Contains(compose, "\n      - \"8443:8443\"\n") {
		t.Fatalf("publish compose:\n%s", compose)
	}
	fallback := "127.0.0.1:" + strconv.Itoa(state.Node.XrayFallbackPort) + ":" + strconv.Itoa(state.Node.XrayFallbackPort)
	if !strings.Contains(compose, fallback) {
		t.Fatalf("fallback port %s missing:\n%s", fallback, compose)
	}
	if f.container == nil || f.container.HostConfig.NetworkMode == networkHost {
		t.Fatalf("container network = %#v", f.container)
	}
	if err = m.checkContainerNetwork(f.container); err != nil {
		t.Fatal(err)
	}
	md, err := m.metadata()
	if err != nil {
		t.Fatal(err)
	}
	if md.Network != networkPublish {
		t.Fatalf("metadata network = %q", md.Network)
	}
}

func TestSetNetworkSwitchesAndRestoresHost(t *testing.T) {
	m, f := deployed(t)
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.SetNetwork(context.Background(), "publish"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readFile(t, m.paths.Compose())), "network_mode: host") {
		t.Fatal("still using host network")
	}
	if f.container.HostConfig.NetworkMode == networkHost {
		t.Fatal("container stayed on host network")
	}
	if err = m.SetNetwork(context.Background(), "publish"); err != nil {
		t.Fatal(err)
	}
	next, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if next.Node.UUID != state.Node.UUID || next.Node.Port != state.Node.Port {
		t.Fatalf("network switch changed node: %+v", next.Node)
	}
	if err = m.SetNetwork(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readFile(t, m.paths.Compose())), "network_mode: host") {
		t.Fatal("did not return to host network")
	}
	if f.container.HostConfig.NetworkMode != networkHost {
		t.Fatalf("container network = %s", f.container.HostConfig.NetworkMode)
	}
	if err = m.SetNetwork(context.Background(), "bridge"); err == nil || !strings.Contains(err.Error(), "host") {
		t.Fatalf("invalid network error = %v", err)
	}
}

func TestDamagedComposeDoesNotDecideOwnership(t *testing.T) {
	m, f := deployed(t)
	good := readFile(t, m.paths.Compose())
	if err := os.WriteFile(m.paths.Compose(), []byte("broken compose"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.checkContainerNetwork(f.container); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.paths.Compose(), good, 0600); err != nil {
		t.Fatal(err)
	}
	f.container.HostConfig.NetworkMode = Project + "_default"
	if err := m.checkContainerNetwork(f.container); err == nil {
		t.Fatal("host compose accepted a bridged container")
	}
}
