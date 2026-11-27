package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"proxydock/internal/docker/model"
)

// Convenience entry points for exercising manager operations in package tests.
func (m *Manager) Prepare(ctx context.Context, version string) error {
	return m.prepareWithNetwork(ctx, version, networkHost, false)
}

func (m *Manager) Upgrade(ctx context.Context, version string) error {
	return m.upgrade(ctx, version, "", "")
}

func (m *Manager) Deploy(ctx context.Context, version string, opts model.GenerateOptions) error {
	if err := validateGenerateInput(opts); err != nil {
		return err
	}
	if err := m.prepareDeployment(ctx, version, "", ""); err != nil {
		return err
	}
	return m.Generate(ctx, opts, true)
}

func (m *Manager) SetPolicy(ctx context.Context, policy model.AccessPolicy) error {
	normalized, err := policy.Normalize()
	if err != nil {
		return err
	}
	return m.updatePolicy(ctx, func(model.AccessPolicy) (model.AccessPolicy, error) {
		return normalized, nil
	})
}

func (m *Manager) ApplyEdited(ctx context.Context, data []byte) error {
	if len(data) == 0 || len(data) > maxConfigSize || !json.Valid(data) {
		return fmt.Errorf("配置必须是大小合理的有效 JSON")
	}
	return m.withLock(ctx, func() error {
		return m.applyEditedLocked(ctx, data)
	})
}
func (m *Manager) applyEditedLocked(ctx context.Context, data []byte) error {
	md, in, err := m.prepareOperation(ctx)
	if err != nil {
		return err
	}
	s, err := m.LoadState()
	if err != nil {
		return err
	}
	data, err = m.prepareEditedConfig(data, s.Node)
	if err != nil {
		return err
	}
	return m.applyConfig(ctx, md, in, s.Node, data, false, "配置已生效")
}

func (m *Manager) Peer(name string) (model.LandingPeer, error) {
	return m.peer(context.Background(), name)
}
