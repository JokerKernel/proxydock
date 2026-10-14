package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openSecureDir(path string, create bool) (*secureDir, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("不安全目录: %q", path)
	}
	current, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	if path == "/" {
		return &secureDir{current}, nil
	}
	for _, name := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		fd, err := syscall.Openat(int(current.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if os.IsNotExist(err) && create {
			if err = syscall.Mkdirat(int(current.Fd()), name, 0700); err == nil || os.IsExist(err) {
				fd, err = syscall.Openat(int(current.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
			}
		}
		current.Close()
		if err != nil {
			return nil, &os.PathError{Op: "open directory", Path: path, Err: err}
		}
		current = os.NewFile(uintptr(fd), path)
	}
	return &secureDir{current}, nil
}

func (d *secureDir) openFile(name string, flags int, mode os.FileMode) (*os.File, error) {
	if name != filepath.Base(name) || name == "." || name == ".." {
		return nil, fmt.Errorf("不安全文件名: %q", name)
	}
	fd, err := syscall.Openat(int(d.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, uint32(mode.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(d.Name(), name), Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(d.Name(), name)), nil
}

func (d *secureDir) createTemp(prefix string) (*os.File, error) {
	for i := 0; i < 10; i++ {
		suffix, err := randomHex(12)
		if err != nil {
			return nil, err
		}
		f, err := d.openFile(prefix+suffix, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			continue
		}
		return f, err
	}
	return nil, fmt.Errorf("无法创建唯一临时文件")
}

func (d *secureDir) unlink(name string) error {
	err := syscall.Unlinkat(int(d.Fd()), name)
	if err != nil {
		return &os.PathError{Op: "remove", Path: filepath.Join(d.Name(), name), Err: err}
	}
	return nil
}

func (d *secureDir) rename(old, next string) error {
	err := syscall.Renameat(int(d.Fd()), old, int(d.Fd()), next)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: old, New: next, Err: err}
	}
	return nil
}
