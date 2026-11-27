package docker

import (
	"fmt"
	"os"
	"path/filepath"
)

// Permanent backups contain only the original config.json, with no encoding or
// management snapshots. Transaction rollback data lives in transactions/.
func (m *Manager) saveConfigBackup(name string, data []byte) error {
	if !backupNamePattern.MatchString(name) {
		return fmt.Errorf("配置备份名称无效")
	}
	dir := filepath.Join(m.paths.Backups(), name)
	return atomicWrite(filepath.Join(dir, "config.json"), data, 0600)
}

func (m *Manager) backupConfiguration(data []byte) (string, error) {
	id, err := randomHex(6)
	if err != nil {
		return "", err
	}
	name := m.now().UTC().Format("20060102T150405.000000000Z") + "-" + id
	if err := m.saveConfigBackup(name, data); err != nil {
		return "", err
	}
	return filepath.Join(m.paths.Backups(), name), nil
}

// Called only after the journal has been removed. This also reclaims snapshots
// left by a crash between committing a transaction and deleting its files.
func (m *Manager) cleanupTransactionSnapshots() error {
	if err := m.checkNoTransaction(); err != nil {
		return err
	}
	if err := safePath(m.paths.Transactions()); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.paths.Transactions())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !backupNamePattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(m.paths.Transactions(), entry.Name())
		if err := removeTemporaryFiles(dir, ".docker-write-"); err != nil {
			return err
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		known, manifest := true, false
		for _, file := range files {
			if !file.Type().IsRegular() || (file.Name() != "snapshot.json" && file.Name() != "config.json") {
				known = false
				break
			}
			manifest = manifest || file.Name() == "snapshot.json"
		}
		if !known {
			continue
		}
		if manifest {
			var snapshots []fileSnapshot
			if err := loadSnapshot(filepath.Join(dir, "snapshot.json"), &snapshots); err != nil {
				return err
			}
			if err := m.checkSnapshotFiles(snapshots); err != nil {
				return err
			}
		}
		if err := safePath(dir); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) checkSnapshotFiles(snapshots []fileSnapshot) error {
	allowed := map[string]bool{}
	for _, path := range m.managedFiles() {
		allowed[path] = true
	}
	if len(snapshots) != len(allowed) {
		return fmt.Errorf("事务快照文件集合无效")
	}
	for _, snapshot := range snapshots {
		if !allowed[snapshot.Path] {
			return fmt.Errorf("快照引用非受管路径")
		}
		delete(allowed, snapshot.Path)
		if !snapshot.Exists && len(snapshot.Data) != 0 {
			return fmt.Errorf("不存在的事务文件不能包含内容")
		}
		if snapshot.Path == m.paths.Config() && snapshot.Exists {
			if len(snapshot.Data) != 0 || snapshot.Reference != "config.json" || len(snapshot.SHA256) != 64 {
				return fmt.Errorf("事务配置必须引用本事务的配置副本")
			}
		} else if snapshot.Reference != "" || snapshot.SHA256 != "" {
			return fmt.Errorf("事务文件不允许引用外部备份: %s", snapshot.Path)
		}
	}
	return nil
}
