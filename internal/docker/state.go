package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"proxydock/internal/docker/model"
)

type Metadata struct {
	SchemaVersion    int       `json:"schema_version"`
	ManagedBy        string    `json:"managed_by"`
	InstanceID       string    `json:"instance_id"`
	ImageRef         string    `json:"image_ref"`
	ImageID          string    `json:"image_id"`
	Version          string    `json:"version"`
	RequestedVersion string    `json:"requested_version"`
	Channel          string    `json:"channel"`
	Network          string    `json:"network"`
	ComposeSHA256    string    `json:"compose_sha256"`
	DesiredState     string    `json:"desired_state"`
	UpdatedAt        time.Time `json:"updated_at"`
}
type State struct {
	SchemaVersion int        `json:"schema_version"`
	ManagedBy     string     `json:"managed_by"`
	InstanceID    string     `json:"instance_id"`
	Node          model.Node `json:"node"`
}

func (m *Manager) metadata() (Metadata, error) {
	var md Metadata
	err := loadJSON(m.paths.Metadata(), &md)
	if err != nil {
		return md, err
	}
	if md.SchemaVersion != 1 || md.ManagedBy != "proxydock" || md.InstanceID != Instance || !validImageRef(md.ImageRef) || !validImageID(md.ImageID) || (md.DesiredState != "stopped" && md.DesiredState != "running") || (md.Channel != "pinned" && md.Channel != "latest" && md.Channel != "pre-release") || !validNetwork(md.Network) {
		return md, fmt.Errorf("Docker 部署记录标识或镜像无效")
	}
	return md, nil
}
func (m *Manager) LoadState() (State, error) {
	var s State
	err := loadJSON(m.paths.State(), &s)
	if err != nil {
		return s, err
	}
	if s.SchemaVersion != 1 || s.ManagedBy != "proxydock" || s.InstanceID != Instance || s.Node.Core != model.CoreXray || s.Node.ManagedBy != "proxydock" || s.Node.SchemaVersion != model.StateSchemaVersion || (s.Node.Security != model.LandingSecurityReality && s.Node.Security != model.LandingSecurityTLS) {
		return s, fmt.Errorf("Docker 节点状态标识无效")
	}
	for _, access := range s.Node.LandingAccesses {
		if (access.Security != model.LandingSecurityReality && access.Security != model.LandingSecurityTLS) || (access.Security == model.LandingSecurityReality && access.Port != 0) {
			return s, fmt.Errorf("落地接入协议或端口无效")
		}
	}
	for _, relay := range s.Node.RelayLinks {
		if err := validatePeer(relay.Upstream); err != nil {
			return s, err
		}
	}
	return s, nil
}
func (m *Manager) managedFiles() []string {
	return []string{m.paths.Compose(), m.paths.Config(), m.paths.Metadata(), m.paths.State()}
}
func (m *Manager) saveNode(n model.Node) error {
	if n.Core != model.CoreXray || n.ManagedBy != "proxydock" || n.SchemaVersion != model.StateSchemaVersion || (n.Security != model.LandingSecurityReality && n.Security != model.LandingSecurityTLS) {
		return fmt.Errorf("拒绝保存非 Docker Xray 节点")
	}
	s := State{SchemaVersion: model.StateSchemaVersion, ManagedBy: "proxydock", InstanceID: Instance, Node: n}
	data, err := jsonBytes(s)
	if err != nil {
		return err
	}
	return m.stateWrite(m.paths.State(), data, 0600)
}
func (m *Manager) checkCompose(md Metadata) error {
	b, err := readPrivate(m.paths.Compose())
	if err != nil {
		return err
	}
	expected, err := m.composeFor(md, nil)
	if err != nil {
		return err
	}
	if digest(b) == md.ComposeSHA256 && bytes.Equal(b, expected) {
		return nil
	}
	return fmt.Errorf("Compose 文件已被外部修改，拒绝按未知定义操作容器")
}
func (m *Manager) managedConfig() (State, []byte, error) {
	s, err := m.LoadState()
	if err != nil {
		return s, nil, err
	}
	b, err := readPrivate(m.paths.Config())
	if err != nil {
		return s, nil, err
	}
	if s.Node.ConfigSHA256 == "" || digest(b) != s.Node.ConfigSHA256 {
		return s, nil, fmt.Errorf("配置与受管状态不一致；请重新生成配置或通过编辑入口恢复一致性")
	}
	return s, b, nil
}

func (m *Manager) readManagedState(ctx context.Context) (State, error) {
	var state State
	err := m.withLock(ctx, func() error {
		if err := m.checkNoTransaction(); err != nil {
			return err
		}
		if err := m.checkOwner(); err != nil {
			return err
		}
		var err error
		state, _, err = m.managedConfig()
		return err
	})
	return state, err
}

// Read-only operations must not recover a transaction or consume partial state.
func (m *Manager) checkNoTransaction() error {
	if err := safePath(m.paths.Journal()); err != nil {
		return err
	}
	if _, err := os.Lstat(m.paths.Journal()); err == nil {
		return fmt.Errorf("存在未完成事务，请先执行 proxydock recover")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
func (m *Manager) hasData() bool {
	for _, p := range []string{m.paths.Data(), m.paths.Deploy(), m.paths.CleanupRecord()} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}
