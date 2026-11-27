//go:build !linux

package docker

import (
	"fmt"
	"os"
)

func openSecureDir(string, bool) (*secureDir, error) {
	return nil, fmt.Errorf("安全文件操作仅支持 Linux")
}
func (d *secureDir) openFile(string, int, os.FileMode) (*os.File, error) {
	return nil, fmt.Errorf("安全文件操作仅支持 Linux")
}
func (d *secureDir) createTemp(string) (*os.File, error) {
	return nil, fmt.Errorf("安全文件操作仅支持 Linux")
}
func (d *secureDir) unlink(string) error    { return fmt.Errorf("安全文件操作仅支持 Linux") }
func (d *secureDir) removeDir(string) error { return fmt.Errorf("安全文件操作仅支持 Linux") }
func (d *secureDir) rename(string, string) error {
	return fmt.Errorf("安全文件操作仅支持 Linux")
}
