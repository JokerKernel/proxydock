//go:build !linux

package docker

func (u *ui) discardPendingInput() {
	if u == nil {
		return
	}
	u.discardBufferedInput()
}
