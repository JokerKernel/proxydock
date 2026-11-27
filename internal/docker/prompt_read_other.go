//go:build !linux

package docker

func (u *ui) readLineInterruptible() (string, error) {
	return u.reader.ReadString('\n')
}
