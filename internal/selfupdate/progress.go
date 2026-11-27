package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"proxydock/internal/terminal"
)

type scriptProgress struct {
	output       io.Writer
	color        bool
	interactive  bool
	width        int
	total, bytes int64
	lastUpdate   time.Time
}

func newScriptProgress(output io.Writer) *scriptProgress {
	progress := &scriptProgress{output: terminal.RawWriter(output), color: terminal.ColorEnabled(output), width: 50}
	if file, ok := progress.output.(*os.File); ok && terminal.WriterInteractive(file) && os.Getenv("TERM") != "dumb" {
		command := exec.Command("stty", "size")
		command.Stdin = file
		if size, err := command.Output(); err == nil {
			progress.interactive = true
			var rows, columns int
			if _, err := fmt.Sscan(string(size), &rows, &columns); err == nil && columns > 0 {
				progress.width = min(progress.width, max(0, columns-30))
			}
		}
	}
	return progress
}

func (p *scriptProgress) Write(data []byte) (int, error) {
	p.bytes += int64(len(data))
	if p.interactive && time.Since(p.lastUpdate) >= 100*time.Millisecond {
		p.render(false)
	}
	return len(data), nil
}

func (p *scriptProgress) finish(ok bool) {
	if p.interactive {
		if ok {
			p.render(true)
			fmt.Fprintln(p.output)
		} else {
			fmt.Fprint(p.output, "\r\x1b[K")
		}
	} else if ok {
		fmt.Fprintf(p.output, "%s 下载完成 · ProxyDock 更新脚本 · %d 字节\n", p.paint("\x1b[32m", "✓"), p.bytes)
	}
}

func (p *scriptProgress) render(complete bool) {
	if !p.interactive {
		return
	}
	p.lastUpdate = time.Now()
	percent, total := 100, p.bytes
	if !complete {
		total = p.total
		if total <= 0 {
			fmt.Fprintf(p.output, "\r已下载 %s\x1b[K", formatBytes(p.bytes))
			return
		}
		percent = min(99, int(float64(p.bytes)*100/float64(total)))
	}
	filled := p.width * percent / 100
	bar := ""
	if p.width > 0 {
		bar = p.paint("\x1b[38;5;208m", strings.Repeat("■", filled)) +
			p.paint("\x1b[2m", strings.Repeat("･", p.width-filled)) + " "
	}
	fmt.Fprintf(p.output, "\r%s%3d%%  %s / %s\x1b[K", bar, percent, formatBytes(p.bytes), formatBytes(total))
}

func (p *scriptProgress) paint(color, value string) string {
	if p.color && value != "" {
		return color + value + "\x1b[0m"
	}
	return value
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(bytes)/(1024*1024))
}
