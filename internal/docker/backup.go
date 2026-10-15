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
		if len(files) == 1 && files[0].Name() == "snapshot.json" && files[0].Type().IsRegular() {
			var snapshots []fileSnapshot
			if err := loadSnapshot(filepath.Join(dir, "snapshot.json"), &snapshots); err != nil {
				return err
			}
			if err := m.checkSnapshotFiles(snapshots); err != nil {
				return err
			}
		} else if len(files) != 0 {
			continue
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
	}
	return nil
}
