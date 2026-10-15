package docker

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"proxydock/internal/docker/model"
)

func cloneNode(n model.Node) (model.Node, error) {
	b, err := jsonBytes(n)
	if err != nil {
		return n, err
	}
	var copy model.Node
	err = decodeNode(b, &copy)
	return copy, err
}
func linkName(name string) error {
	if !namePattern.MatchString(name) || strings.HasPrefix(name, "access-") || name == "direct" || name == "private-block" || name == "fallback-in" || name == "fallback-out" {
		return fmt.Errorf("线路名称无效或保留")
	}
	return nil
}
func validatePeer(p model.LandingPeer) error {
	if p.Core != model.CoreXray || p.Flow != model.VisionFlow {
		return fmt.Errorf("上游节点 core 或 flow 无效")
	}
	if !validServer(p.Server) || p.Port < 1 || p.Port > 65535 || !hostname(p.SNI, true) || !uuidPattern.MatchString(p.UUID) {
		return fmt.Errorf("上游节点地址/端口/SNI/UUID 无效")
	}
	security := p.Security
	if security == "tls" {
		if !model.ValidCertificateSHA256(p.CertificateSHA256) || !model.ValidCertificatePublicKeySHA256(p.CertificatePublicKeySHA256) {
			return fmt.Errorf("上游 TLS 节点缺少证书固定指纹")
		}
	} else if security == "reality" {
		b, err := base64.RawURLEncoding.DecodeString(p.PublicKey)
		if err != nil || len(b) != 32 {
			return fmt.Errorf("上游 REALITY 公钥无效")
		}
		b, err = hex.DecodeString(p.ShortID)
		if err != nil || len(b) == 0 || len(b) > 8 {
			return fmt.Errorf("上游 REALITY short ID 无效")
		}
	} else {
		return fmt.Errorf("上游协议无效")
	}
	return nil
}
func (m *Manager) patchLinks(ctx context.Context, md Metadata, in *inspection, old, next model.Node, b []byte) error {
	oldRender, err := m.renderNodeReferences(old)
	if err != nil {
		return err
	}
	newRender, err := m.renderNode(next)
	if err != nil {
		return err
	}
	patched, err := m.codec.PatchLinks(b, oldRender, newRender)
	if err != nil {
		return err
	}
	if old.AccessPolicy.Active() || next.AccessPolicy.Active() {
		patched, err = m.codec.PatchAccessPolicy(patched, oldRender, newRender)
		if err != nil {
			return err
		}
	}
	return m.applyConfig(ctx, md, in, next, patched, false, "线路已更新")
}
func (m *Manager) AddLanding(ctx context.Context, name, security string, port int) (model.LandingPeer, error) {
	var peer model.LandingPeer
	if err := linkName(name); err != nil {
		return peer, err
	}
	security = model.NormalizeLandingSecurity(security)
	sharePrimary := security == model.LandingSecurityCurrent
	if security != "reality" && security != "tls" && !sharePrimary {
		return peer, fmt.Errorf("落地协议必须为 current、reality 或 tls")
	}
	if port != 0 && (security != "tls" || port < 1024 || port > 65535) {
		return peer, fmt.Errorf("TLS 落地端口无效")
	}
	err := m.withLock(ctx, func() error {
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		s, b, err := m.managedConfig()
		if err != nil {
			return err
		}
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		for _, a := range next.LandingAccesses {
			if a.Name == name {
				return fmt.Errorf("落地接入已存在")
			}
		}
		for _, r := range next.RelayLinks {
			if r.Name == name {
				return fmt.Errorf("名称已被中转线路占用")
			}
		}
		if sharePrimary {
			security = model.NormalizeLandingSecurity(next.Security)
		}
		if security == "reality" && next.IsTLS() {
			return fmt.Errorf("TLS 主节点不支持共享 REALITY 落地，请选择 tls")
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		access := model.LandingAccess{Name: name, UserName: "landing-" + name, UUID: id, Security: security, Enabled: true, UpdatedAt: m.now().UTC()}
		certDir := ""
		success := false
		defer func() {
			if !success && certDir != "" {
				if _, err := os.Lstat(m.paths.Journal()); os.IsNotExist(err) {
					_ = os.RemoveAll(certDir)
				}
			}
		}()
		if security == "tls" && !sharePrimary {
			if port == 0 {
				port, err = m.freeHighPort(next.Port)
				if err != nil {
					return err
				}
			}
			if port < 1024 || port > 65535 || slices.Contains(nodePorts(next), port) {
				return fmt.Errorf("TLS 落地端口无效或重复")
			}
			cert, e := m.newCertificate(name)
			if e != nil {
				return e
			}
			certDir = filepath.Dir(cert.CertificateFile)
			access.SNI = cert.SNI
			access.Port = port
			access.CertificateFile = cert.CertificateFile
			access.KeyFile = cert.KeyFile
			access.CertificateSHA256 = cert.CertificateSHA256
			access.CertificatePublicKeySHA256 = cert.CertificatePublicKeySHA256
		}
		next.LandingAccesses = append(next.LandingAccesses, access)
		if err = m.patchLinks(ctx, md, in, s.Node, next, b); err != nil {
			return err
		}
		success = true
		peer = landingPeer(next, access)
		return nil
	})
	return peer, err
}
func landingPeer(n model.Node, a model.LandingAccess) model.LandingPeer {
	p := model.LandingPeer{Name: a.Name, Core: "xray", Security: model.NormalizeLandingSecurity(a.Security), Server: n.Server, Port: n.Port, SNI: n.SNI, UUID: a.UUID, Flow: model.VisionFlow}
	if a.UsesPrimaryInbound() {
		p.Security = model.NormalizeLandingSecurity(n.Security)
		if n.IsTLS() {
			if n.TLSCertificate != nil {
				p.CertificateSHA256 = n.TLSCertificate.CertificateSHA256
				p.CertificatePublicKeySHA256 = n.TLSCertificate.CertificatePublicKeySHA256
			}
		} else {
			p.PublicKey = n.PublicKey
			p.ShortID = n.ShortID
		}
	} else {
		p.Port = a.Port
		p.SNI = a.SNI
		p.CertificateSHA256 = a.CertificateSHA256
		p.CertificatePublicKeySHA256 = a.CertificatePublicKeySHA256
	}
	return p
}
func (m *Manager) AddRelay(ctx context.Context, name string, peer model.LandingPeer) error {
	if err := linkName(name); err != nil {
		return err
	}
	if err := validatePeer(peer); err != nil {
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
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		for _, r := range next.RelayLinks {
			if r.Name == name {
				return fmt.Errorf("中转线路已存在")
			}
		}
		for _, a := range next.LandingAccesses {
			if a.Name == name {
				return fmt.Errorf("名称已被落地接入占用")
			}
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		next.RelayLinks = append(next.RelayLinks, model.RelayLink{Name: name, UserName: "relay-" + name, UUID: id, Enabled: true, Upstream: peer, UpdatedAt: m.now().UTC()})
		return m.patchLinks(ctx, md, in, s.Node, next, b)
	})
}
func (m *Manager) ChangeLink(ctx context.Context, kind, name, action string) error {
	if kind != "landing" && kind != "relay" {
		return fmt.Errorf("线路类型无效")
	}
	if action != "remove" && action != "enable" && action != "disable" {
		return fmt.Errorf("线路操作无效")
	}
	if err := linkName(name); err != nil {
		return err
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
		next, err := cloneNode(s.Node)
		if err != nil {
			return err
		}
		found := false
		switch kind {
		case "landing":
			for i, a := range next.LandingAccesses {
				if a.Name == name {
					found = true
					if action != "remove" && a.Enabled == (action == "enable") {
						return nil
					}
					if action == "remove" {
						next.LandingAccesses = append(next.LandingAccesses[:i], next.LandingAccesses[i+1:]...)
					} else {
						next.LandingAccesses[i].Enabled = action == "enable"
						next.LandingAccesses[i].UpdatedAt = m.now().UTC()
					}
					break
				}
			}
		case "relay":
			for i, r := range next.RelayLinks {
				if r.Name == name {
					found = true
					if action != "remove" && r.Enabled == (action == "enable") {
						return nil
					}
					if action == "remove" {
						next.RelayLinks = append(next.RelayLinks[:i], next.RelayLinks[i+1:]...)
					} else {
						next.RelayLinks[i].Enabled = action == "enable"
						next.RelayLinks[i].UpdatedAt = m.now().UTC()
					}
					break
				}
			}
		default:
			return fmt.Errorf("线路类型无效")
		}
		if !found {
			return fmt.Errorf("未找到线路 %s", name)
		}
		md, in, err := m.prepareOperation(ctx)
		if err != nil {
			return err
		}
		return m.patchLinks(ctx, md, in, s.Node, next, b)
	})
}
func (m *Manager) Peer(name string) (model.LandingPeer, error) {
	return m.peer(context.Background(), name)
}

func (m *Manager) peer(ctx context.Context, name string) (model.LandingPeer, error) {
	s, err := m.readManagedState(ctx)
	if err != nil {
		return model.LandingPeer{}, err
	}
	for _, a := range s.Node.LandingAccesses {
		if a.Name == name {
			if !a.Enabled {
				return model.LandingPeer{}, fmt.Errorf("落地接入已禁用")
			}
			if !a.UsesPrimaryInbound() {
				if err := m.validateCertificate(a); err != nil {
					return model.LandingPeer{}, err
				}
			} else if s.Node.IsTLS() {
				if s.Node.TLSCertificate == nil {
					return model.LandingPeer{}, fmt.Errorf("TLS 节点缺少证书")
				}
				if err := m.validateCertificate(*s.Node.TLSCertificate); err != nil {
					return model.LandingPeer{}, err
				}
			}
			return landingPeer(s.Node, a), nil
		}
	}
	return model.LandingPeer{}, fmt.Errorf("未找到落地接入")
}
