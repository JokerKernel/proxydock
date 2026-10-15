package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func tmpfsOption(path, size string, uid, gid int) string {
	opt := fmt.Sprintf("%s:rw,noexec,nosuid,nodev,size=%s,mode=0700", path, size)
	if uid == 0 && gid == 0 {
		return opt
	}
	return fmt.Sprintf("%s,uid=%d,gid=%d", opt, uid, gid)
}

type containerIdentity struct {
	uid     int
	gid     int
	pinRoot bool
}

func parseRunUser(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "root", "0", "0:0":
		return true, nil
	case "default", "image":
		return false, nil
	default:
		return false, fmt.Errorf("运行用户无效，请使用 default 或 root")
	}
}

func runUserLabel(root bool) string {
	if root {
		return "root"
	}
	return "镜像默认"
}

func (m *Manager) resolvedImageUser(ctx context.Context) (int, int, error) {
	id, err := m.containerIdentity(ctx)
	return id.uid, id.gid, err
}

// Before deployment metadata exists, temporary commands run as root so they can
// read root-owned 0600 files. Afterwards the service user decides. A non-root
// deployment records the user id read from the container.
func (m *Manager) containerIdentity(ctx context.Context) (containerIdentity, error) {
	md, err := m.metadata()
	if err != nil {
		if os.IsNotExist(err) {
			return containerIdentity{pinRoot: true}, nil
		}
		return containerIdentity{}, err
	}
	if md.RunAsRoot {
		return containerIdentity{pinRoot: true}, nil
	}
	spec := md.ImageUser
	if spec == "" && md.ImageID != "" {
		spec, err = m.inspectImageUser(ctx, md.ImageID)
		if err != nil {
			return containerIdentity{}, err
		}
	}
	uid, gid, ok := parseImageUser(spec)
	if !ok {
		return containerIdentity{}, fmt.Errorf("镜像用户 %q 无法识别", spec)
	}
	return containerIdentity{uid: uid, gid: gid}, nil
}

func (m *Manager) inspectImageUser(ctx context.Context, imageID string) (string, error) {
	b, err := m.docker(ctx, "image", "inspect", imageID)
	if err != nil {
		return "", err
	}
	var images []struct {
		Config struct {
			User string
		}
	}
	if err = json.Unmarshal(b, &images); err != nil || len(images) != 1 {
		return "", fmt.Errorf("镜像详情无效")
	}
	return normalizeImageUser(images[0].Config.User)
}

func (m *Manager) grantImageUser(ctx context.Context, paths ...string) error {
	uid, gid, err := m.resolvedImageUser(ctx)
	if err != nil || uid == 0 && gid == 0 {
		return err
	}
	for _, path := range paths {
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err = chownTree(path, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func chownTree(root string, uid, gid int) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 才能把挂载文件交给镜像用户 %d:%d", uid, gid)
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("受管路径不能是符号链接: %s", path)
		}
		return os.Lchown(path, uid, gid)
	})
}

// defineUserFromContainer stores the container's numeric user as ImageUser and
// aligns the log tmpfs with that uid. An empty or root user carries no image
// id. RunAsRoot stays 0:0 even if the container still shows another uid.
func (m *Manager) defineUserFromContainer(md *Metadata, in *inspection) error {
	if in == nil || md == nil || md.RunAsRoot {
		return nil
	}
	spec := strings.TrimSpace(in.Config.User)
	if spec == "" {
		return nil
	}
	uid, gid, ok := parseImageUser(spec)
	if !ok {
		return fmt.Errorf("容器用户 %q 无法识别", spec)
	}
	if uid == 0 && gid == 0 {
		return nil
	}
	normalized := fmt.Sprintf("%d:%d", uid, gid)
	if md.ImageUser == normalized {
		return nil
	}
	previous := *md
	md.ImageUser = normalized
	next, err := m.composeFor(*md, nil)
	if err != nil {
		*md = previous
		return err
	}
	current, err := readPrivate(m.paths.Compose())
	if err != nil {
		*md = previous
		return err
	}
	if string(current) != string(next) {
		if err = atomicWrite(m.paths.Compose(), next, 0600); err != nil {
			*md = previous
			return err
		}
	}
	md.ComposeSHA256 = digest(next)
	md.UpdatedAt = m.now().UTC()
	if err = saveJSON(m.paths.Metadata(), md); err != nil {
		if string(current) != string(next) {
			_ = atomicWrite(m.paths.Compose(), current, 0600)
		}
		*md = previous
		return err
	}
	return nil
}

func (m *Manager) reconcileContainerUser(ctx context.Context, md *Metadata, in *inspection) (bool, error) {
	current, legacy, err := m.classifyCompose(*md)
	if err != nil || current || !legacy {
		return false, err
	}
	// Learn the image user before rewriting compose, so the log tmpfs is
	// created for that uid. The legacy comparison itself always uses the old pin.
	if md.ImageUser == "" && md.ImageID != "" {
		spec, err := m.inspectImageUser(ctx, md.ImageID)
		if err != nil {
			return false, err
		}
		md.ImageUser = spec
	}
	if err = m.refreshCompose(ctx, *md, in); err != nil {
		return false, err
	}
	next, err := m.metadata()
	if err != nil {
		return false, err
	}
	*md = next
	return true, nil
}
