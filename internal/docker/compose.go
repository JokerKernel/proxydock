package docker

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"proxydock/internal/docker/model"
)

const (
	networkHost    = "host"
	networkPublish = "publish"
)

//go:embed templates/compose.yaml.tmpl
var composeTemplate string

var parsedComposeTemplate = template.Must(template.New("compose").Parse(composeTemplate))
var shaPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/xtls/xray-core@sha256:[0-9a-f]{64}$`)

type composeView struct {
	Image   string
	Network string
}

type publishedPort struct {
	HostIP    string
	Host      int
	Container int
}

func composeNetworkBlock(mode string, ports []publishedPort) string {
	if mode == networkHost {
		return "    network_mode: host\n"
	}
	if len(ports) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("    ports:\n")
	for _, port := range ports {
		b.WriteString("      - \"")
		b.WriteString(port.spec())
		b.WriteString("\"\n")
	}
	return b.String()
}

func (p publishedPort) spec() string {
	if p.HostIP == "" {
		return fmt.Sprintf("%d:%d", p.Host, p.Container)
	}
	return fmt.Sprintf("%s:%d:%d", p.HostIP, p.Host, p.Container)
}

func validImageRef(s string) bool { return imagePattern.MatchString(s) }
func validImageID(s string) bool  { return shaPattern.MatchString(s) }

func canonicalNetwork(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", networkHost:
		return networkHost
	case networkPublish:
		return networkPublish
	default:
		return ""
	}
}

func validNetwork(value string) bool { return canonicalNetwork(value) != "" }

func normalizeNetwork(value string) (string, error) {
	mode := canonicalNetwork(value)
	if mode == "" {
		return "", fmt.Errorf("网络方式无效，请使用 host 或 publish")
	}
	return mode, nil
}

func networkLabel(value string) string {
	if canonicalNetwork(value) == networkPublish {
		return "端口映射"
	}
	return "宿主机网络"
}

func storedNetwork(mode string) string {
	if canonicalNetwork(mode) == networkPublish {
		return networkPublish
	}
	return ""
}

func renderCompose(image, network string, ports []publishedPort) ([]byte, error) {
	if !validImageRef(image) {
		return nil, fmt.Errorf("镜像必须是官方仓库的固定 digest")
	}
	mode := canonicalNetwork(network)
	if mode == "" {
		return nil, fmt.Errorf("网络方式无效，请使用 host 或 publish")
	}
	view := composeView{Image: image, Network: composeNetworkBlock(mode, ports)}
	var b bytes.Buffer
	if err := parsedComposeTemplate.Execute(&b, view); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func publishedPorts(n model.Node) []publishedPort {
	return composePorts(n, false)
}

// legacyPublishedPorts is used only to recognize previously generated files.
func legacyPublishedPorts(n model.Node) []publishedPort {
	return composePorts(n, true)
}

func composePorts(n model.Node, legacy bool) []publishedPort {
	var ports []publishedPort
	seen := map[int]bool{}
	add := func(port int, hostIP string) {
		if port < 1 || port > 65535 || seen[port] {
			return
		}
		seen[port] = true
		ports = append(ports, publishedPort{HostIP: hostIP, Host: port, Container: port})
	}
	add(n.Port, "")
	for _, access := range n.LandingAccesses {
		if access.Enabled && !access.UsesPrimaryInbound() {
			add(access.Port, "")
		}
	}
	if legacy && n.XrayFallbackGuard {
		add(n.XrayFallbackPort, "127.0.0.1")
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].HostIP != ports[j].HostIP {
			return ports[i].HostIP < ports[j].HostIP
		}
		return ports[i].Host < ports[j].Host
	})
	return ports
}

// managedComposeNetwork recognizes only a compose file this program wrote.
// Damaged or foreign text must not decide whether an existing container is owned.
func managedComposeNetwork(b []byte) (string, []publishedPort, bool) {
	mode, ports, err := parseComposeNetwork(b)
	if err != nil {
		return "", nil, false
	}
	if mode == networkHost || len(ports) > 0 || bytes.Contains(b, []byte("container_name: "+Container)) {
		return mode, ports, true
	}
	return "", nil, false
}

func parseComposeNetwork(b []byte) (string, []publishedPort, error) {
	mode := networkPublish
	var ports []publishedPort
	for _, line := range strings.Split(string(b), "\n") {
		trim := strings.TrimSpace(line)
		if trim == "network_mode: host" {
			mode = networkHost
			continue
		}
		if !strings.HasPrefix(trim, `- "`) || !strings.HasSuffix(trim, `"`) {
			continue
		}
		spec := strings.TrimSuffix(strings.TrimPrefix(trim, `- "`), `"`)
		port, err := parsePortSpec(spec)
		if err != nil {
			return "", nil, err
		}
		ports = append(ports, port)
	}
	if mode == networkHost && len(ports) > 0 {
		return "", nil, fmt.Errorf("编排同时使用宿主机网络和端口映射")
	}
	return mode, ports, nil
}

func parsePortSpec(spec string) (publishedPort, error) {
	parts := strings.Split(spec, ":")
	var hostIP, hostText, containerText string
	switch len(parts) {
	case 2:
		hostText, containerText = parts[0], parts[1]
	case 3:
		hostIP, hostText, containerText = parts[0], parts[1], parts[2]
	default:
		return publishedPort{}, fmt.Errorf("端口映射 %q 无效", spec)
	}
	host, err := strconv.Atoi(hostText)
	if err != nil || host < 1 || host > 65535 {
		return publishedPort{}, fmt.Errorf("端口映射 %q 无效", spec)
	}
	container, err := strconv.Atoi(containerText)
	if err != nil || container < 1 || container > 65535 || container != host {
		return publishedPort{}, fmt.Errorf("端口映射 %q 无效", spec)
	}
	if hostIP != "" && hostIP != "127.0.0.1" {
		return publishedPort{}, fmt.Errorf("端口映射 %q 无效", spec)
	}
	return publishedPort{HostIP: hostIP, Host: host, Container: container}, nil
}

func (m *Manager) composeFor(md Metadata, n *model.Node) ([]byte, error) {
	return m.composeDefinition(md, n, false)
}

func (m *Manager) composeDefinition(md Metadata, n *model.Node, legacy bool) ([]byte, error) {
	mode := canonicalNetwork(md.Network)
	if mode == "" {
		return nil, fmt.Errorf("网络方式无效，请使用 host 或 publish")
	}
	var ports []publishedPort
	if mode == networkPublish {
		if n == nil {
			s, err := m.LoadState()
			if err == nil {
				n = &s.Node
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
		if n != nil {
			ports = publishedPorts(*n)
			if legacy {
				ports = legacyPublishedPorts(*n)
			}
		}
	}
	return renderCompose(md.ImageRef, mode, ports)
}

func (m *Manager) writeManagedCompose(md *Metadata, n *model.Node) (bool, error) {
	next, err := m.composeFor(*md, n)
	if err != nil {
		return false, err
	}
	current, err := readPrivate(m.paths.Compose())
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err == nil && string(current) == string(next) {
		md.ComposeSHA256 = digest(next)
		return false, nil
	}
	if err = atomicWrite(m.paths.Compose(), next, 0600); err != nil {
		return false, err
	}
	md.ComposeSHA256 = digest(next)
	m.op(phaseApply, levelStep, "写入编排", m.paths.Compose())
	return true, nil
}
