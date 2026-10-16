//go:build linux

package docker

import (
	"bytes"
	"context"
	"testing"

	"proxydock/internal/terminal"
)

func TestStreamingCommandInheritsTerminalWithColorOutput(t *testing.T) {
	for _, color := range []bool{false, true} {
		name := "plain"
		if color {
			name = "color"
		}
		t.Run(name, func(t *testing.T) {
			_, slave := openMenuPTY(t)
			out := terminal.NewColorWriter(slave, color)
			// A forwarding pipe carries bytes but prevents Docker from detecting
			// the terminal and rendering its native download progress.
			if err := (ExecRunner{}).RunStreaming(context.Background(), out, out, "sh", "-c", "[ -t 1 ] || exit 81; [ -t 2 ] || exit 82"); err != nil {
				t.Fatalf("streaming command did not inherit terminal outputs: %v", err)
			}
		})
	}
}

func TestStreamingCommandPreservesExternalOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	out := terminal.NewColorWriter(&stdout, true)
	errOut := terminal.NewColorWriter(&stderr, true)
	wantOut := "\rDownloading [==> ] 1MB/2MB\x1b[1A\rDownload complete\n[状态] 原始输出\n"
	wantErr := "\r[警告] 原始错误\n"
	if err := (ExecRunner{}).RunStreaming(context.Background(), out, errOut, "sh", "-c", `printf '%s' "$1"; printf '%s' "$2" >&2`, "sh", wantOut, wantErr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != wantOut || stderr.String() != wantErr {
		t.Fatalf("external output was modified: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
