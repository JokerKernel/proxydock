package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"proxydock/internal/docker/export"
)

func (m *Manager) Client(ctx context.Context, format, name string) ([]byte, error) {
	switch format {
	case "native", "clash":
	default:
		return nil, fmt.Errorf("客户端格式无效，可选 native 或 clash")
	}
	var output []byte
	err := m.withLock(ctx, func() error {
		if err := m.checkNoTransaction(); err != nil {
			return err
		}
		if err := m.checkOwner(); err != nil {
			return err
		}
		s, _, err := m.managedConfig()
		if err != nil {
			return err
		}
		n := s.Node
		if name != "" {
			found := false
			for _, a := range n.LandingAccesses {
				if a.Name == name && a.Enabled {
					found = true
					n.UUID = a.UUID
					if !a.UsesPrimaryInbound() {
						n.Security = "tls"
						n.Port = a.Port
						n.SNI = a.SNI
						n.TLSCertificate = &a
					}
					break
				}
			}
			if !found {
				for _, r := range n.RelayLinks {
					if r.Name == name && r.Enabled {
						found = true
						n.UUID = r.UUID
						break
					}
				}
			}
			if !found {
				return fmt.Errorf("未找到启用的落地/中转线路")
			}
		}
		if n.IsTLS() {
			if n.TLSCertificate == nil {
				return fmt.Errorf("TLS 节点缺少证书")
			}
			if err = m.validateCertificate(*n.TLSCertificate); err != nil {
				return err
			}
		}
		switch format {
		case "native":
			output, err = m.codec.RenderClient(n)
		case "clash":
			output, err = export.RenderClient(n)
		}
		return err
	})
	return output, err
}
func (m *Manager) WriteExport(path string, b []byte, force bool) error {
	if err := m.rootCheck(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		path = absolute
	}
	if err := safePath(path); err != nil {
		return err
	}
	dir, err := openSecureDir(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if !force {
		f, err := dir.openFile(filepath.Base(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		if e := f.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		return dir.Sync()
	}
	// Export destinations may be user-owned directories; do not chmod their parent.
	f, err := dir.createTemp(".docker-export-")
	if err != nil {
		return err
	}
	tmp := filepath.Base(f.Name())
	defer dir.unlink(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err := dir.rename(tmp, filepath.Base(path)); err != nil {
		return err
	}
	return dir.Sync()
}
