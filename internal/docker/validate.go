package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) coreRun(ctx context.Context, label opLabel, md Metadata, mounts []string, command ...string) ([]byte, error) {
	id, err := m.imageIdentity(ctx, md)
	if err != nil {
		return nil, err
	}
	return m.coreRunAs(ctx, label, md.ImageID, id, mounts, command...)
}

func (m *Manager) coreRunAs(ctx context.Context, label opLabel, imageID string, id containerIdentity, mounts []string, command ...string) (output []byte, runErr error) {
	if !validImageID(imageID) {
		return nil, fmt.Errorf("无效的本机镜像 ID")
	}
	suffix, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	name := Container + "-check-" + suffix
	args := []string{"run", "--rm", "--pull", "never", "--name", name, "--label", "io.proxydock.managed=true", "--label", "io.proxydock.instance=" + Instance, "--label", "io.proxydock.role=validator", "--network", "host", "--read-only", "--cap-drop", "ALL", "--cap-add", "NET_BIND_SERVICE", "--security-opt", "no-new-privileges:true"}
	if id.pinRoot {
		args = append(args, "--user", "0:0")
	}
	args = append(args, "--tmpfs", tmpfsOption("/usr/local/etc/xray", "1m", id.uid, id.gid), "--tmpfs", "/tmp:rw,noexec,nosuid,nodev,size=16m,mode=1777", "--tmpfs", tmpfsOption("/var/log/xray", "16m", id.uid, id.gid))
	args = append(args, mounts...)
	args = append(args, imageID)
	args = append(args, command...)
	defer func() {
		// --rm removes a successfully completed container. Inspect only on failure,
		// when cancellation or a daemon error might have left it running.
		if runErr == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, err := m.docker(cleanupCtx, "container", "inspect", name)
		if err != nil {
			return
		}
		var ins []inspection
		if json.Unmarshal(b, &ins) == nil && len(ins) == 1 && ins[0].Config.Labels["io.proxydock.instance"] == Instance && ins[0].Config.Labels["io.proxydock.role"] == "validator" {
			_, _ = m.docker(cleanupCtx, "container", "rm", "--force", "--volumes", ins[0].ID)
		}
	}()
	return m.dockerOp(ctx, label, args...)
}
func (m *Manager) validateBytes(ctx context.Context, md Metadata, data []byte) error {
	if len(data) == 0 || len(data) > maxConfigSize || !json.Valid(data) {
		return fmt.Errorf("配置必须是大小合理的有效 JSON")
	}
	if err := privateDir(m.paths.Staging()); err != nil {
		return err
	}
	if err := privateDir(m.paths.TLS()); err != nil {
		return err
	}
	id, err := m.imageIdentity(ctx, md)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(m.paths.Staging(), "validation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = saveJSON(filepath.Join(dir, ".proxydock-validation.json"), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		return err
	}
	path, tls := filepath.Join(dir, "config.json"), filepath.Join(dir, "tls")
	if err = atomicWrite(path, data, 0600); err != nil {
		return err
	}
	// A new image may use a different uid. Never chown the live certificates
	// during preflight: the old service still needs them if validation fails.
	if err = copyValidationTLS(m.paths.TLS(), tls); err != nil {
		return err
	}
	if err = m.grantContainerIdentity(id, path, tls); err != nil {
		return err
	}
	for _, path := range []string{path, tls} {
		if strings.ContainsAny(path, ",\n\r") {
			return fmt.Errorf("挂载路径含不支持的字符")
		}
		if err = safePath(path); err != nil {
			return err
		}
	}
	_, err = m.coreRunAs(ctx, opLabel{phase: phaseCheck, level: levelStep, name: "校验 Xray 配置"}, md.ImageID, id, []string{"--mount", "type=bind,src=" + path + ",dst=/run/proxydock/config.json,readonly", "--mount", "type=bind,src=" + tls + ",dst=/etc/xray/tls,readonly"}, "run", "-test", "-config", "/run/proxydock/config.json")
	if err != nil {
		m.op(phaseCheck, levelError, "校验结果", err.Error())
		return fmt.Errorf("Docker Xray 配置校验失败: %w", err)
	}
	m.op(phaseCheck, levelResult, "校验结果", "通过")
	return nil
}

func copyValidationTLS(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return privateDir(target)
		}
		data, err := readPrivate(path)
		if err != nil {
			return err
		}
		return atomicWrite(target, data, 0600)
	})
}
