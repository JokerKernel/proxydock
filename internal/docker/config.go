package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"proxydock/internal/docker/model"
	"proxydock/internal/docker/xray/accesspolicy"
)

func nodePorts(n model.Node) []int {
	seen := map[int]bool{n.Port: true}
	if n.XrayFallbackGuard {
		seen[n.XrayFallbackPort] = true
	}
	for _, a := range n.LandingAccesses {
		if a.Enabled && !a.UsesPrimaryInbound() {
			seen[a.Port] = true
		}
	}
	var ports []int
	for p := range seen {
		if p > 0 {
			ports = append(ports, p)
		}
	}
	sort.Ints(ports)
	return ports
}

// hostPorts excludes listeners that are confined to the container loopback.
func hostPorts(n model.Node, network string) []int {
	if canonicalNetwork(network) == networkHost {
		return nodePorts(n)
	}
	var ports []int
	for _, p := range publishedPorts(n) {
		ports = append(ports, p.Host)
	}
	return ports
}
func (m *Manager) prepareOperation(ctx context.Context) (Metadata, *inspection, error) {
	if err := m.CheckReady(ctx); err != nil {
		return Metadata{}, nil, err
	}
	if err := m.recoverLocked(ctx); err != nil {
		return Metadata{}, nil, err
	}
	if err := m.checkOwner(); err != nil {
		return Metadata{}, nil, err
	}
	md, err := m.metadata()
	if err != nil {
		return md, nil, err
	}
	if err = m.checkCompose(md); err != nil {
		return md, nil, err
	}
	if err = m.checkProject(ctx); err != nil {
		return md, nil, err
	}
	in, err := m.inspect(ctx)
	if err == nil && in != nil && in.State.Paused {
		err = fmt.Errorf("实例已暂停，请先恢复容器运行状态")
	}
	if err == nil && in != nil && in.Image != md.ImageID {
		err = fmt.Errorf("实际容器镜像与记录不一致")
	}
	return md, in, err
}
func (m *Manager) prepareDeployment(ctx context.Context, version, network string) error {
	md, err := m.metadata()
	if os.IsNotExist(err) {
		if strings.TrimSpace(network) == "" {
			network = networkHost
		}
		return m.prepareWithNetwork(ctx, version, network)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(network) != "" {
		mode, err := normalizeNetwork(network)
		if err != nil {
			return err
		}
		if mode != canonicalNetwork(md.Network) {
			keep := version
			if keep == "" {
				switch md.Channel {
				case "latest":
					keep = "latest"
				case "pre-release":
					keep = "pre-release"
				default:
					keep = md.RequestedVersion
				}
			}
			if err = m.upgrade(ctx, keep, mode); err != nil {
				return err
			}
			md, err = m.metadata()
			if err != nil {
				return err
			}
		}
	}
	if version != "" {
		if err = checkPreparedVersion(md, version); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) Generate(ctx context.Context, opts model.GenerateOptions, start bool) error {
	if err := validateGenerateInput(opts); err != nil {
		return err
	}
	return m.withLock(ctx, func() error {
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		oldState, err := m.LoadState()
		hasOld := err == nil
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if hasOld {
			if _, _, err = m.managedConfig(); err != nil {
				return err
			}
		} else if _, err = os.Lstat(m.paths.Config()); !os.IsNotExist(err) {
			return fmt.Errorf("存在无状态的配置，拒绝覆盖")
		}
		n := oldState.Node
		n.Core = model.CoreXray
		n.ManagedBy = "proxydock"
		n.SchemaVersion = 1
		n.UpdatedAt = m.now().UTC()
		n.CoreVersion = md.Version
		security := opts.Security
		if security == "" && hasOld {
			security = n.Security
		}
		if security == "" {
			security = model.LandingSecurityReality
		}
		n.Security = model.NormalizeLandingSecurity(security)
		if opts.DropLinks {
			n.LandingAccesses = nil
			n.RelayLinks = nil
		} else if hasOld && n.Security != model.NormalizeLandingSecurity(oldState.Node.Security) && (len(n.LandingAccesses) > 0 || len(n.RelayLinks) > 0) {
			return fmt.Errorf("切换主节点协议前请移除线路，或显式使用 --drop-links")
		}
		if opts.Server != "" {
			n.Server = opts.Server
		}
		if opts.Port != 0 {
			n.Port = opts.Port
		}
		if n.Port == 0 {
			n.Port = 443
		}
		if opts.UserName != "" {
			n.UserName = opts.UserName
		}
		if n.UserName == "" {
			n.UserName = "one"
		}
		if opts.InboundTag != "" {
			n.InboundTag = opts.InboundTag
		}
		if n.InboundTag == "" {
			n.InboundTag = "docker-one"
		}
		var newCert string
		success := false
		defer func() {
			if !success && newCert != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(newCert)
				}
			}
		}()
		if n.IsTLS() {
			n.Target = ""
			n.XrayFallbackGuard = false
			n.XrayFallbackPort = 0
			n.XrayFallbackHTTPDomain = false
			n.XrayFallbackExactDomain = false
			if n.TLSCertificate == nil || opts.RotateCertificate || !oldState.Node.IsTLS() {
				suffix, err := randomHex(10)
				if err != nil {
					return err
				}
				cert, err := m.newCertificate("main-" + suffix)
				if err != nil {
					return err
				}
				newCert = filepath.Dir(cert.CertificateFile)
				n.TLSCertificate = &cert
			}
			n.SNI = n.TLSCertificate.SNI
		} else {
			n.TLSCertificate = nil
			if opts.SNI != "" {
				n.SNI = opts.SNI
			}
			if opts.Target != "" {
				n.Target = opts.Target
			}
			if n.Target == "" && n.SNI != "" {
				n.Target = n.SNI + ":443"
			}
			if !hasOld || opts.StandardConfig || opts.XrayFallbackGuard {
				n.XrayFallbackGuard = !opts.StandardConfig
			}
			if n.XrayFallbackGuard {
				if opts.XrayFallbackPort != 0 {
					n.XrayFallbackPort = opts.XrayFallbackPort
				}
				if n.XrayFallbackPort == 0 {
					n.XrayFallbackPort, err = m.freeHighPort(n.Port)
					if err != nil {
						return err
					}
				}
			} else {
				n.XrayFallbackPort = 0
			}
			if !n.XrayFallbackGuard {
				n.XrayFallbackHTTPDomain = false
				n.XrayFallbackExactDomain = false
			} else if opts.ApplyFallbackPolicy {
				n.XrayFallbackHTTPDomain = opts.XrayFallbackHTTPDomain
				n.XrayFallbackExactDomain = opts.XrayFallbackExactDomain
			} else {
				if opts.XrayFallbackHTTPDomain {
					n.XrayFallbackHTTPDomain = true
				}
				if opts.XrayFallbackExactDomain {
					n.XrayFallbackExactDomain = true
				}
			}
			if n.XrayFallbackGuard && n.InboundTag == "fallback-in" {
				return fmt.Errorf("入站标签 %q 已由 Xray 回落防偷跑入站使用，请选择其他标签", n.InboundTag)
			}
			if err = m.targetCheck(ctx, n.Target, n.SNI, n.Server); err != nil {
				return err
			}
		}
		if !hasOld || opts.RotateCredentials || n.UUID == "" {
			n.UUID, err = newUUID()
			if err != nil {
				return err
			}
			if !n.IsTLS() {
				keys, e := m.keyPair(ctx, md.ImageID)
				if e != nil {
					return e
				}
				n.PrivateKey, n.PublicKey = keys.Private, keys.Public
				n.ShortID, err = randomHex(8)
				if err != nil {
					return err
				}
			}
		} else if !n.IsTLS() && n.PrivateKey == "" {
			keys, e := m.keyPair(ctx, md.ImageID)
			if e != nil {
				return e
			}
			n.PrivateKey, n.PublicKey = keys.Private, keys.Public
			n.ShortID, err = randomHex(8)
			if err != nil {
				return err
			}
		}
		if n.IsTLS() {
			n.PrivateKey = ""
			n.PublicKey = ""
			n.ShortID = ""
		}
		if err = validateNode(n); err != nil {
			return err
		}
		rendered, err := m.renderNode(n)
		if err != nil {
			return err
		}
		b, err := m.codec.RenderServer(rendered)
		if err != nil {
			return err
		}
		if n.AccessPolicy.Active() {
			baseline := rendered
			baseline.AccessPolicy = model.AccessPolicy{}
			unrestricted, renderErr := m.codec.RenderServer(baseline)
			if renderErr != nil {
				return renderErr
			}
			n.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(unrestricted, rendered)
			if err != nil {
				return err
			}
		}
		securityLabel := n.Security
		if securityLabel == "" {
			securityLabel = "reality"
		}
		m.op(phaseCheck, levelInfo, "生成配置", fmt.Sprintf("%s，端口 %d", securityLabel, n.Port))
		if err = m.applyConfig(ctx, md, in, n, b, start || !hasOld, fmt.Sprintf("已生成，端口 %d", n.Port)); err != nil {
			return err
		}
		success = true
		return nil
	})
}

// preferredListenPort uses 443 when it is free or already belongs to this node.
// Otherwise it keeps an existing high port, or allocates a new random one.
func (m *Manager) preferredListenPort(current int) (int, error) {
	if current == 443 || m.portFree(443) == nil {
		return 443, nil
	}
	if current >= model.FallbackPortMin && current <= model.FallbackPortMax {
		return current, nil
	}
	port, err := m.freeHighPort(443)
	if err != nil {
		return 0, fmt.Errorf("443 已被占用，且没有可用的高位端口")
	}
	return port, nil
}

func (m *Manager) freeHighPort(exclude int) (int, error) {
	for i := 0; i < 100; i++ {
		s, err := randomHex(2)
		if err != nil {
			return 0, err
		}
		v, _ := strconv.ParseInt(s, 16, 64)
		port := 30000 + int(v)%35000
		if port != exclude && m.portFree(port) == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("找不到可用回落端口")
}
func (m *Manager) applyConfig(ctx context.Context, md Metadata, in *inspection, n model.Node, b []byte, start bool, outcome string) error {
	if outcome == "" {
		outcome = "配置已生效"
	}
	if err := validateNode(n); err != nil {
		return err
	}
	old, readErr := readPrivate(m.paths.Config())
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	configChanged := readErr != nil || string(old) != string(b)
	if !configChanged && !start {
		if s, err := m.LoadState(); err == nil {
			compare := n
			compare.ConfigSHA256 = digest(b)
			compare.UpdatedAt = s.Node.UpdatedAt
			if reflect.DeepEqual(compare, s.Node) {
				m.op(phaseResult, levelResult, "更新结果", "配置未变化")
				return nil
			}
		}
	}
	if err := m.validateBytes(ctx, md.ImageID, b); err != nil {
		return &configurationValidationError{err}
	}
	j, err := m.begin("config", in)
	if err != nil {
		return err
	}
	fail := func(e error) error { return m.rollback(j, e) }
	running := start || in != nil && in.State.Running
	var oldPorts []int
	if s, e := m.LoadState(); e == nil {
		oldPorts = hostPorts(s.Node, md.Network)
	}
	ports := hostPorts(n, md.Network)
	changed := !slices.Equal(oldPorts, ports)
	if running && changed {
		if in != nil && in.State.Running {
			detail := formatPorts(ports)
			if len(oldPorts) > 0 {
				detail = formatPorts(oldPorts) + " → " + detail
			}
			m.op(phaseApply, levelInfo, "端口变化", detail)
			if _, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "停止容器"}, "stop", Service); err != nil {
				return fail(err)
			}
		}
		for _, p := range ports {
			if err = m.portFree(p); err != nil {
				return fail(err)
			}
		}
		m.op(phaseApply, levelResult, "检查端口", "可用")
	}
	if err = atomicWrite(m.paths.Config(), b, 0600); err != nil {
		return fail(err)
	}
	m.op(phaseApply, levelStep, "写入配置", m.paths.Config())
	composeChanged, err := m.writeManagedCompose(&md, &n)
	if err != nil {
		return fail(err)
	}
	if composeChanged {
		if _, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "校验编排"}, "config", "--quiet"); err != nil {
			return fail(fmt.Errorf("Compose 定义校验失败: %w", err))
		}
	}
	if err = m.phase(&j, "files-committed"); err != nil {
		return fail(err)
	}
	if composeChanged && in != nil {
		args := []string{"up", "--no-deps", "--pull", "never", "--force-recreate"}
		if running {
			args = append(args, "-d")
		} else {
			args = append(args, "--no-start")
		}
		args = append(args, Service)
		if _, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "重建容器"}, args...); err != nil {
			return fail(err)
		}
		if running {
			m.op(phaseApply, levelInfo, "稳定检查", healthStartDetail(ports))
			if err = m.healthCheck(ctx, ports, md.ImageID); err != nil {
				return fail(err)
			}
			m.op(phaseApply, levelResult, "运行状态", "运行中，监听端口正常")
		}
	} else if running && (configChanged || start) {
		if in == nil {
			_, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "启动容器"}, "up", "-d", "--no-deps", "--pull", "never", Service)
		} else {
			_, err = m.composeOp(ctx, opLabel{phase: phaseApply, level: levelStep, name: "重启容器"}, "restart", Service)
		}
		if err != nil {
			return fail(err)
		}
		m.op(phaseApply, levelInfo, "稳定检查", healthStartDetail(ports))
		if err = m.healthCheck(ctx, ports, md.ImageID); err != nil {
			return fail(err)
		}
		m.op(phaseApply, levelResult, "运行状态", "运行中，监听端口正常")
	} else if !running {
		m.op(phaseApply, levelInfo, "运行状态", "未运行，配置已写入，启动后生效")
	}
	if running {
		md.DesiredState = "running"
	} else {
		md.DesiredState = "stopped"
	}
	if err = m.phase(&j, "runtime-applied"); err != nil {
		return fail(err)
	}
	n.ConfigSHA256 = digest(b)
	n.UpdatedAt = m.now().UTC()
	if err = m.saveNode(n); err != nil {
		return fail(err)
	}
	m.op(phaseApply, levelStep, "保存状态", m.paths.State())
	md.UpdatedAt = m.now().UTC()
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		return fail(err)
	}
	if err = m.commit(j); err != nil {
		return fail(err)
	}
	m.op(phaseResult, levelResult, "更新结果", outcome)
	return nil
}

type configurationValidationError struct{ error }

func (e *configurationValidationError) Unwrap() error { return e.error }

func accessPolicySummary(policy model.AccessPolicy) string {
	if policy.Disabled {
		return "已关闭，名单保留"
	}
	white := len(policy.Whitelist) > 0
	black := len(policy.Blacklist) > 0
	if policy.EffectiveMode() == model.AccessModeWhitelist {
		if black {
			return "白名单优先，黑名单继续拦截"
		}
		return "白名单优先，现有拦截规则兜底"
	}
	if white && !black {
		return "白名单优先，现有拦截规则兜底"
	}
	if white && black {
		return "白名单优先，黑名单继续拦截"
	}
	if black {
		return "黑名单拦截，其余按现有规则放行"
	}
	return "未启用访问限制"
}

func accessPolicyOutcome(policy model.AccessPolicy) string {
	if policy.Disabled {
		return "访问策略已关闭"
	}
	if policy.EffectiveMode() == model.AccessModeWhitelist || (len(policy.Whitelist) > 0 && len(policy.Blacklist) == 0) {
		return "白名单已更新"
	}
	return "访问策略已更新"
}

func settingSummary(setting, value string) string {
	switch setting {
	case "dns":
		return "DNS " + value
	case "log":
		return "日志级别 " + value
	case "outbound-ip":
		return "出站 IP " + value
	case "fallback-ip":
		return "回落 IP " + value
	default:
		return setting + " " + value
	}
}

func formatPorts(ports []int) string {
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(port)
	}
	return strings.Join(parts, "、")
}
func (m *Manager) PatchSetting(ctx context.Context, setting, value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	var values []string
	switch setting {
	case "dns":
		values = m.codec.DNSProfiles()
	case "log":
		values = m.codec.LogLevels()
	case "outbound-ip", "fallback-ip":
		values = m.codec.OutboundIPStrategies()
	default:
		return fmt.Errorf("设置类型无效")
	}
	if !slices.Contains(values, value) {
		return fmt.Errorf("%s 设置值 %q 无效", setting, value)
	}
	return m.withLock(ctx, func() error {
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		if err := m.checkOwner(); err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		var patched []byte
		switch setting {
		case "dns":
			patched, err = m.codec.PatchDNSProfile(b, value)
		case "log":
			patched, err = m.codec.PatchLogLevel(b, value)
		case "outbound-ip":
			patched, err = m.codec.PatchOutboundIPStrategy(b, value)
		case "fallback-ip":
			if !s.Node.XrayFallbackGuard {
				return fmt.Errorf("未启用回落防护")
			}
			patched, err = m.codec.PatchFallbackIPStrategy(b, value)
		default:
			return fmt.Errorf("设置类型无效")
		}
		if err != nil {
			return err
		}
		if s.Node.AccessPolicy.Active() {
			patched, err = m.codec.PatchAccessPolicy(patched, s.Node, s.Node)
			if err != nil {
				return err
			}
		}
		if sameJSON(b, patched) {
			m.op(phaseResult, levelResult, "更新结果", "配置未变化")
			return nil
		}
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		m.op(phaseCheck, levelInfo, "修改配置", settingSummary(setting, value))
		return m.applyConfig(ctx, md, in, s.Node, patched, false, "配置已生效")
	})
}

// Read and change the policy under the same lock, after transaction recovery.
func (m *Manager) updatePolicy(ctx context.Context, change func(model.AccessPolicy) (model.AccessPolicy, error)) error {
	return m.withLock(ctx, func() error {
		if err := m.recoverLocked(ctx); err != nil {
			return err
		}
		if err := m.checkOwner(); err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		old := s.Node
		current, err := old.AccessPolicy.Normalize()
		if err != nil {
			return err
		}
		policy, err := change(current)
		if err != nil {
			return err
		}
		normalized, err := policy.Normalize()
		if err != nil {
			return err
		}
		if reflect.DeepEqual(current, normalized) {
			m.op(phaseResult, levelResult, "更新结果", "访问策略未变化")
			return nil
		}
		s.Node.AccessPolicy = normalized
		if !old.AccessPolicy.Active() && normalized.Active() {
			s.Node.AccessPolicySniffing, err = accesspolicy.SnapshotSniffing(b, s.Node)
			if err != nil {
				return err
			}
		}
		patched, err := m.codec.PatchAccessPolicy(b, old, s.Node)
		if err != nil {
			return err
		}
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		m.op(phaseCheck, levelInfo, "应用访问策略", accessPolicySummary(normalized))
		return m.applyConfig(ctx, md, in, s.Node, patched, false, accessPolicyOutcome(normalized))
	})
}
func (m *Manager) Reset(ctx context.Context, opts model.ResetOptions) error {
	if !opts.Credentials && !opts.RotateCertificate && opts.SNI == "" && opts.Target == "" {
		return fmt.Errorf("请选择轮换凭据、轮换证书或更新 SNI/target")
	}
	if err := validateGenerateInput(model.GenerateOptions{SNI: opts.SNI, Target: opts.Target}); err != nil {
		return err
	}
	return m.withLock(ctx, func() error {
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		old := s.Node
		n := old
		rotateCredentials := opts.Credentials
		if rotateCredentials {
			n.UUID, err = newUUID()
			if err != nil {
				return err
			}
		}
		newCert := ""
		success := false
		defer func() {
			if !success && newCert != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(newCert)
				}
			}
		}()
		if n.IsTLS() {
			if opts.SNI != "" || opts.Target != "" {
				return fmt.Errorf("TLS 节点请通过轮换证书更新 SNI")
			}
			if opts.RotateCertificate {
				suffix, e := randomHex(10)
				if e != nil {
					return e
				}
				cert, e := m.newCertificate("main-" + suffix)
				if e != nil {
					return e
				}
				n.TLSCertificate = &cert
				n.SNI = cert.SNI
				newCert = filepath.Dir(cert.CertificateFile)
			}
		} else {
			if opts.RotateCertificate {
				return fmt.Errorf("只有 TLS 节点可以轮换证书")
			}
			if rotateCredentials {
				keys, e := m.keyPair(ctx, md.ImageID)
				if e != nil {
					return e
				}
				n.PrivateKey, n.PublicKey = keys.Private, keys.Public
				n.ShortID, err = randomHex(8)
				if err != nil {
					return err
				}
			}
			if opts.SNI != "" {
				n.SNI = opts.SNI
				if opts.Target == "" {
					n.Target = n.SNI + ":443"
				}
			}
			if opts.Target != "" {
				n.Target = opts.Target
			}
			if opts.SNI != "" || opts.Target != "" {
				if err = m.targetCheck(ctx, n.Target, n.SNI, n.Server); err != nil {
					return err
				}
			}
		}
		oldRender, err := m.renderNodeReferences(old)
		if err != nil {
			return err
		}
		newRender, err := m.renderNode(n)
		if err != nil {
			return err
		}
		patched, err := m.codec.PatchServer(b, oldRender, newRender, true)
		if err != nil {
			return err
		}
		if n.AccessPolicy.Active() {
			patched, err = m.codec.PatchAccessPolicy(patched, oldRender, newRender)
			if err != nil {
				return err
			}
		}
		if err = m.applyConfig(ctx, md, in, n, patched, false, "配置已生效"); err != nil {
			return err
		}
		success = true
		return nil
	})
}
func (m *Manager) ServerConfig() ([]byte, error) {
	if err := m.rootCheck(); err != nil {
		return nil, err
	}
	return readPrivate(m.paths.Config())
}
func (m *Manager) Validate(ctx context.Context) error {
	return m.withLock(ctx, func() error {
		if err := m.checkNoTransaction(); err != nil {
			return err
		}
		if err := m.checkOwner(); err != nil {
			return err
		}
		if err := m.checkDocker(); err != nil {
			return err
		}
		md, err := m.metadata()
		if err != nil {
			return err
		}
		b, err := readPrivate(m.paths.Config())
		if err != nil {
			return err
		}
		return m.validateBytes(ctx, md.ImageID, b)
	})
}
func (m *Manager) prepareEditedConfig(data []byte, node model.Node) ([]byte, error) {
	original, err := m.renderNode(node)
	if err != nil {
		return nil, err
	}
	// Retain explicit managed identity, allowing independent unknown JSON fields.
	if _, err = m.codec.PatchServer(data, original, original, false); err != nil {
		return nil, fmt.Errorf("编辑破坏了受管节点身份: %w", err)
	}
	if _, err = m.codec.PatchLinks(data, original, original); err != nil {
		return nil, fmt.Errorf("编辑破坏了受管线路: %w", err)
	}
	if node.AccessPolicy.Active() {
		var e error
		data, e = m.codec.PatchAccessPolicy(data, original, original)
		if e != nil {
			return nil, e
		}
	}
	if err = managedEndpointMatches(data, original); err != nil {
		return nil, err
	}
	return data, nil
}

type managedInbound struct {
	Tag      string
	Listen   string
	Protocol string
	Port     int
	Stream   map[string]json.RawMessage `json:"streamSettings"`
	Settings json.RawMessage
}

func managedEndpointMatches(data []byte, n model.Node) error {
	var root struct {
		Inbounds []managedInbound
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	if err := managedPrimaryEndpointMatches(root.Inbounds, n); err != nil {
		return err
	}
	var primary managedInbound
	for _, in := range root.Inbounds {
		if in.Tag == n.InboundTag {
			primary = in
		}
	}
	for _, access := range n.LandingAccesses {
		if access.UsesPrimaryInbound() {
			if err := managedClientMatches(primary.Settings, access.UserName, access.UUID, access.Enabled); err != nil {
				return err
			}
			continue
		}
		tag := model.LandingTLSInboundTag(access.Name)
		if !access.Enabled {
			for _, in := range root.Inbounds {
				if in.Tag == tag {
					return fmt.Errorf("已禁用的受管 TLS 落地入站仍存在: %s", access.Name)
				}
			}
			continue
		}
		endpoint := model.Node{InboundTag: tag, Port: access.Port, Security: "tls", UUID: access.UUID, UserName: access.UserName, TLSCertificate: &access}
		if err := managedPrimaryEndpointMatches(root.Inbounds, endpoint); err != nil {
			return fmt.Errorf("TLS 落地接入 %s: %w", access.Name, err)
		}
	}
	for _, relay := range n.RelayLinks {
		if err := managedClientMatches(primary.Settings, relay.UserName, relay.UUID, relay.Enabled); err != nil {
			return err
		}
	}
	if n.XrayFallbackGuard {
		host, rawPort, err := net.SplitHostPort(n.Target)
		if err != nil {
			return err
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			return err
		}
		found := 0
		for _, in := range root.Inbounds {
			if in.Tag != "fallback-in" {
				continue
			}
			found++
			var settings struct {
				Address string
				Port    int
				Network string
			}
			if json.Unmarshal(in.Settings, &settings) != nil || in.Listen != "127.0.0.1" || in.Port != n.XrayFallbackPort || in.Protocol != "tunnel" || settings.Address != host || settings.Port != port || settings.Network != "tcp" {
				return fmt.Errorf("受管回落入站或目标已改变，请使用生成配置或重置入口")
			}
		}
		if found != 1 {
			return fmt.Errorf("受管回落入站必须唯一存在")
		}
	}
	return nil
}

func managedClientMatches(data []byte, name, uuid string, enabled bool) error {
	var settings struct {
		Clients []struct {
			ID    string
			Email string
			Flow  string
		}
	}
	if json.Unmarshal(data, &settings) != nil {
		return fmt.Errorf("受管客户端列表无效")
	}
	found := 0
	for _, client := range settings.Clients {
		if client.Email == name || client.ID == uuid {
			found++
			if client.ID != uuid || client.Email != name || client.Flow != model.VisionFlow {
				return fmt.Errorf("受管用户 %s 的 UUID、用户名或 flow 已改变，请使用专用入口", name)
			}
		}
	}
	want := 0
	if enabled {
		want = 1
	}
	if found != want {
		return fmt.Errorf("受管用户 %s 的数量与启用状态不一致", name)
	}
	return nil
}

func managedPrimaryEndpointMatches(inbounds []managedInbound, n model.Node) error {
	found := 0
	for _, in := range inbounds {
		if in.Tag == n.InboundTag {
			found++
			if in.Port != n.Port {
				return fmt.Errorf("请通过生成配置入口修改受管端口")
			}
			var network string
			if in.Listen != "0.0.0.0" || in.Protocol != "vless" || json.Unmarshal(in.Stream["network"], &network) != nil || network != "raw" && network != "tcp" {
				return fmt.Errorf("受管监听地址、协议或传输方式已改变")
			}
			if err := managedClientMatches(in.Settings, n.UserName, n.UUID, true); err != nil {
				return err
			}
			var security string
			if json.Unmarshal(in.Stream["security"], &security) != nil || security != model.NormalizeLandingSecurity(n.Security) {
				return fmt.Errorf("受管入站协议已改变")
			}
			if n.IsTLS() {
				var tls struct {
					Certificates []struct {
						Cert string `json:"certificateFile"`
						Key  string `json:"keyFile"`
					}
				}
				if json.Unmarshal(in.Stream["tlsSettings"], &tls) != nil || len(tls.Certificates) != 1 || n.TLSCertificate == nil || tls.Certificates[0].Cert != n.TLSCertificate.CertificateFile || tls.Certificates[0].Key != n.TLSCertificate.KeyFile {
					return fmt.Errorf("受管 TLS 证书引用已改变")
				}
			} else {
				var reality struct {
					Private  string   `json:"privateKey"`
					Names    []string `json:"serverNames"`
					ShortIDs []string `json:"shortIds"`
					Target   string   `json:"target"`
				}
				if json.Unmarshal(in.Stream["realitySettings"], &reality) != nil || reality.Private != n.PrivateKey || !slices.Contains(reality.Names, n.SNI) || !slices.Contains(reality.ShortIDs, n.ShortID) {
					return fmt.Errorf("受管 REALITY 凭据或 SNI 已改变")
				}
				wantTarget := n.Target
				if n.XrayFallbackGuard {
					wantTarget = net.JoinHostPort("127.0.0.1", strconv.Itoa(n.XrayFallbackPort))
				}
				if reality.Target != wantTarget {
					return fmt.Errorf("请通过生成配置入口修改受管 REALITY target")
				}
			}
		}
	}
	if found != 1 {
		return fmt.Errorf("受管入站必须唯一存在")
	}
	return nil
}
