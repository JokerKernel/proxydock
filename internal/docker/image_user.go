package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Named users are resolved from the image's account files without starting it.
// This also works for distroless images that have no shell or id executable.
func (m *Manager) resolveImageUser(ctx context.Context, imageID, spec string) (string, error) {
	if _, _, ok := parseImageUser(spec); ok {
		return normalizeImageUser(spec)
	}
	if !validImageID(imageID) {
		return "", fmt.Errorf("无效的本机镜像 ID")
	}
	user, group, hasGroup := strings.Cut(strings.TrimSpace(spec), ":")
	if user == "" || strings.ContainsAny(user+group, ":\n\r\x00") || hasGroup && group == "" {
		return "", fmt.Errorf("镜像用户 %q 无法识别", spec)
	}
	suffix, err := randomHex(12)
	if err != nil {
		return "", err
	}
	name := Container + "-check-" + suffix
	// Use our generated name for cleanup even if create was interrupted after
	// the daemon created the container but before returning its id.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		in, err := m.inspectContainerWithCheck(cleanupCtx, name, func(in *inspection) error {
			labels := in.Config.Labels
			if in.Name != "/"+name || labels["io.proxydock.managed"] != "true" || labels["io.proxydock.instance"] != Instance || labels["io.proxydock.role"] != "validator" {
				return fmt.Errorf("镜像账户校验容器归属不匹配")
			}
			return nil
		})
		if err == nil {
			_, _ = m.docker(cleanupCtx, "container", "rm", "--force", "--volumes", in.ID)
		}
	}()
	if _, err = m.docker(ctx, "create", "--pull", "never", "--name", name,
		"--label", "io.proxydock.managed=true", "--label", "io.proxydock.instance="+Instance,
		"--label", "io.proxydock.role=validator", "--network", "none", "--read-only",
		"--user", "0:0", "--entrypoint", "/usr/local/bin/xray", imageID, "version"); err != nil {
		return "", err
	}
	uid, uidErr := strconv.Atoi(user)
	gid := uid
	if uidErr != nil {
		passwd, err := m.imageAccountFile(ctx, name, "/etc/passwd")
		if err != nil {
			return "", err
		}
		found := false
		for _, line := range strings.Split(string(passwd), "\n") {
			fields := strings.Split(line, ":")
			if len(fields) < 7 || fields[0] != user {
				continue
			}
			var gidErr error
			uid, uidErr = strconv.Atoi(fields[2])
			gid, gidErr = strconv.Atoi(fields[3])
			if uidErr != nil || gidErr != nil {
				return "", fmt.Errorf("镜像用户 %q 的 UID/GID 无效", user)
			}
			found = true
			break
		}
		if !found {
			return "", fmt.Errorf("镜像中不存在用户 %q", user)
		}
	}
	if hasGroup {
		var groupErr error
		gid, groupErr = strconv.Atoi(group)
		if groupErr != nil {
			groups, err := m.imageAccountFile(ctx, name, "/etc/group")
			if err != nil {
				return "", err
			}
			found := false
			for _, line := range strings.Split(string(groups), "\n") {
				fields := strings.Split(line, ":")
				if len(fields) >= 3 && fields[0] == group {
					gid, groupErr = strconv.Atoi(fields[2])
					found = groupErr == nil
					break
				}
			}
			if !found {
				return "", fmt.Errorf("镜像中不存在有效组 %q", group)
			}
		}
	}
	return normalizeImageUser(fmt.Sprintf("%d:%d", uid, gid))
}

func (m *Manager) imageAccountFile(ctx context.Context, container, path string) ([]byte, error) {
	data, err := m.docker(ctx, "cp", container+":"+path, "-")
	if err != nil {
		return nil, err
	}
	reader := tar.NewReader(bytes.NewReader(data))
	header, err := reader.Next()
	const maxAccountSize = 1 << 20
	if err != nil || header.Typeflag != tar.TypeReg || header.Size > maxAccountSize {
		return nil, fmt.Errorf("镜像账户文件 %s 无效", path)
	}
	return io.ReadAll(io.LimitReader(reader, maxAccountSize))
}
