package docker

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

var landingPasteDirectoryPattern = regexp.MustCompile(`^landing-paste-[0-9]+$`)

// The directory lease survives in the editor if the manager is killed. Later
// maintenance can reclaim abandoned sessions without deleting an active edit.
func (m *Manager) cleanupLandingPasteDirs() error {
	entries, err := os.ReadDir(m.paths.Staging())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !landingPasteDirectoryPattern.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(m.paths.Staging(), entry.Name())
		var marker ownership
		if err := loadJSON(filepath.Join(dir, ".proxydock-landing.json"), &marker); err != nil || marker.ManagedBy != "proxydock" || marker.SchemaVersion != 1 || marker.InstanceID != Instance {
			continue
		}
		fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		lease := os.NewFile(uintptr(fd), dir)
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			_ = lease.Close()
			continue
		}
		if err == nil {
			err = os.RemoveAll(dir)
		}
		closeErr := lease.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return nil
}
