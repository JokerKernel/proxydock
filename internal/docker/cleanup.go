package docker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const cleanupWritePrefix = ".xray-cleanup-write-"

var cleanupWritePattern = regexp.MustCompile(`^\.xray-cleanup-write-[a-f0-9]{24}$`)

func (m *Manager) saveCleanupRecord() error {
	data, err := jsonBytes(ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance})
	if err != nil {
		return err
	}
	return atomicWriteWithPrefix(m.paths.CleanupRecord(), data, 0600, cleanupWritePrefix)
}

// Called with the instance lock held after recovery or cleanup checks.
func (m *Manager) cleanupRecordTemps() (int, error) {
	path := filepath.Dir(m.paths.CleanupRecord())
	if err := safePath(path); err != nil {
		return 0, err
	}
	dir, err := openSecureDir(path, false)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return 0, err
	}
	retained, removed := 0, false
	for _, entry := range entries {
		if !cleanupWritePattern.MatchString(entry.Name()) {
			continue
		}
		full := filepath.Join(path, entry.Name())
		if reason := cleanupTempRejection(dir, entry); reason != "" {
			retained++
			m.op(phaseApply, levelWarn, "保留残留", full+"（"+reason+"）")
			continue
		}
		if err := dir.unlink(entry.Name()); err != nil {
			return retained, err
		}
		removed = true
		m.op(phaseApply, levelStep, "回收临时文件", full)
	}
	if removed {
		if err := dir.Sync(); err != nil {
			return retained, err
		}
	}
	return retained, nil
}

func cleanupTempRejection(dir *secureDir, entry os.DirEntry) string {
	if !entry.Type().IsRegular() {
		return "不是普通文件，无法确认安全删除"
	}
	f, err := dir.openFile(entry.Name(), os.O_RDONLY, 0)
	if err != nil {
		return "无法读取文件：" + err.Error()
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "无法检查文件：" + err.Error()
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return "类型、权限或所有者不符合本实例临时文件"
	}
	return ""
}

// Only remove empty shared parents, through their verified directory handles.
func (m *Manager) cleanupEmptyParents() error {
	for _, path := range []string{filepath.Dir(m.paths.Deploy()), filepath.Dir(m.paths.Data()), m.paths.resolve("/var/lib/proxydock")} {
		if err := safePath(path); err != nil {
			return err
		}
		dir, err := openSecureDir(filepath.Dir(path), false)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = dir.removeDir(filepath.Base(path))
		if err == nil {
			err = dir.Sync()
			dir.Close()
			if err != nil {
				return fmt.Errorf("同步父目录失败: %w", err)
			}
			m.op(phaseApply, levelStep, "回收空目录", path)
			continue
		}
		dir.Close()
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			m.op(phaseApply, levelInfo, "保留目录", path+"（包含其他文件）")
			continue
		}
		if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
