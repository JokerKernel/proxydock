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
	defaultNetwork = networkPublish
)

//go:embed templates/compose.yaml.tmpl
var composeTemplate string

var parsedComposeTemplate = template.Must(template.New("compose").Parse(composeTemplate))
var shaPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var imagePattern = regexp.MustCompile(`^ghcr\.io/xtls/xray-core@sha256:[0-9a-f]{64}$`)

type composeView struct {
	Image    string
	Network  string
	LogTmpfs string
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
	case networkHost:
		return networkHost
	case networkPublish:
		return networkPublish
	default:
		return ""
	}
}

func validNetwork(value string) bool { return value == networkHost || value == networkPublish }

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

func renderCompose(image, network string, ports []publishedPort) ([]byte, error) {
	if !validImageRef(image) {
		return nil, fmt.Errorf("镜像必须是官方仓库的固定 digest")
	}
	mode := canonicalNetwork(network)
	if mode == "" {
		return nil, fmt.Errorf("网络方式无效，请使用 host 或 publish")
	}
	view := composeView{Image: image, Network: composeNetworkBlock(mode, ports), LogTmpfs: logTmpfs(0, 0)}
	var b bytes.Buffer
	if err := parsedComposeTemplate.Execute(&b, view); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func publishedPorts(n model.Node) []publishedPort {
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
		}
	}
	uid, gid, ok := parseImageUser(md.ImageUser)
	if md.ImageUser != "" && !ok {
		return nil, fmt.Errorf("镜像用户 %q 无法识别", md.ImageUser)
	}
	rendered, err := renderCompose(md.ImageRef, mode, ports)
	if err != nil {
		return nil, err
	}
	if md.RunAsRoot {
		pinned, ok := pinRootLine(rendered)
		if !ok {
			return nil, fmt.Errorf("编排模板无法固定 root 用户")
		}
		return pinned, nil
	}
	if uid == 0 && gid == 0 {
		return rendered, nil
	}
	old := logTmpfs(0, 0)
	next := logTmpfs(uid, gid)
	if !bytes.Contains(rendered, []byte(old)) {
		return nil, fmt.Errorf("编排模板缺少日志临时目录")
	}
	return bytes.Replace(rendered, []byte(old), []byte(next), 1), nil
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

func logTmpfs(uid, gid int) string {
	opt := "/var/log/xray:rw,noexec,nosuid,nodev,size=16m,mode=0700"
	if uid == 0 && gid == 0 {
		return opt
	}
	return fmt.Sprintf("%s,uid=%d,gid=%d", opt, uid, gid)
}

func parseImageUser(spec string) (uid int, gid int, ok bool) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "", "0", "root", "0:0", "root:root", "0:root", "root:0":
		return 0, 0, true
	}
	user, group, hasGroup := strings.Cut(spec, ":")
	uid, err := strconv.Atoi(user)
	if err != nil || uid < 0 {
		return 0, 0, false
	}
	gid = uid
	if hasGroup {
		gid, err = strconv.Atoi(group)
		if err != nil || gid < 0 {
			return 0, 0, false
		}
	}
	return uid, gid, true
}

func normalizeImageUser(spec string) (string, error) {
	uid, gid, ok := parseImageUser(spec)
	if !ok {
		return "", fmt.Errorf("镜像用户 %q 无法识别", spec)
	}
	return fmt.Sprintf("%d:%d", uid, gid), nil
}

// Previous releases pinned the container to root. That exact file is still ours.
func legacyUserCompose(current []byte) ([]byte, bool) {
	return pinRootLine(current)
}

func pinRootLine(compose []byte) ([]byte, bool) {
	const marker = "    read_only: true\n"
	const line = "    user: \"0:0\"\n"
	if bytes.Contains(compose, []byte("\n    user:")) || !bytes.Contains(compose, []byte(marker)) {
		return nil, false
	}
	return bytes.Replace(compose, []byte(marker), []byte(line+marker), 1), true
}

func (m *Manager) classifyCompose(md Metadata) (current bool, legacy bool, err error) {
	b, err := readPrivate(m.paths.Compose())
	if err != nil {
		return false, false, err
	}
	expected, err := m.composeFor(md, nil)
	if err != nil {
		return false, false, err
	}
	if digest(b) == md.ComposeSHA256 && bytes.Equal(b, expected) {
		return true, false, nil
	}
	previous := md
	previous.ImageUser = "0:0"
	previous.RunAsRoot = false
	base, err := m.composeFor(previous, nil)
	if err != nil {
		return false, false, err
	}
	old, ok := legacyUserCompose(base)
	if ok && digest(b) == md.ComposeSHA256 && bytes.Equal(b, old) {
		return false, true, nil
	}
	return false, false, fmt.Errorf("Compose 文件已被外部修改，拒绝按未知定义操作容器")
}

// Root and the historical 0:0 pin are both managed. Any other user was not written by us.
func managedContainerUser(user string) bool {
	switch strings.TrimSpace(user) {
	case "", "0:0":
		return true
	default:
		return false
	}
}
