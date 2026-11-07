// Package selfupdate downloads and runs ProxyDock's current management script.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"proxydock/internal/terminal"
)

type Options struct {
	Uninstall bool
	AssumeYes bool
	Version   string
}

type Runner interface {
	Run(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) error
}

type Updater struct {
	Client         *http.Client
	ScriptURL      string
	AllowedHosts   []string
	Runner         Runner
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

func (u Updater) Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.Uninstall && opts.Version != "" {
		return fmt.Errorf("版本选项仅用于更新程序")
	}
	stdout, stderr := u.Stdout, u.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = stdout
	}
	operation := "安装/更新"
	if opts.Uninstall {
		operation = "卸载"
	}
	scriptURL := u.ScriptURL
	if scriptURL == "" {
		scriptURL = defaultScriptURL
	}
	detail := func(name, text string) {
		// Match ProxyForge's 22-column management preparation output.
		padding := strings.Repeat(" ", max(0, 22-terminal.DisplayWidth(name)))
		fmt.Fprintf(stderr, "%s%s │ %s\n", name, padding, text)
	}
	detail("准备程序管理脚本", "下载并验证 ProxyDock "+operation+"脚本")
	detail("脚本来源", scriptURL)
	script, err := u.downloadScript(ctx, scriptURL)
	if err != nil {
		return fmt.Errorf("准备 ProxyDock %s脚本: %w", operation, err)
	}
	file, err := os.CreateTemp("", "proxydock-management-*.sh")
	if err != nil {
		return fmt.Errorf("准备安装脚本: %w", err)
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0700); err == nil {
		_, err = file.Write(script.content)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写入安装脚本: %w", err)
	}
	detail("校验脚本语法", "bash -n "+file.Name())
	if output, err := exec.CommandContext(ctx, "bash", "-n", file.Name()).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("安装脚本 bash -n 校验失败: %s: %w", output, err)
	}
	detail("脚本校验结果", "bash -n，校验通过")
	detail("重定向后地址", script.finalURL)
	detail("脚本大小", fmt.Sprintf("%d bytes", len(script.content)))
	detail("脚本 SHA-256", script.sha256)
	detail("启动程序管理", "ProxyDock "+operation+"流程")
	args := []string{file.Name()}
	if opts.Uninstall {
		args = append(args, "uninstall")
	}
	if opts.AssumeYes {
		args = append(args, "--yes")
	}
	runner := u.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	// The installer renders its own colors and checks terminal descriptors for
	// download progress. Pass through the actual output instead of color pipes.
	stdout, stderr = terminal.RawWriter(stdout), terminal.RawWriter(stderr)
	command := "bash"
	if opts.Version != "" {
		// Override only the child environment; concurrent updates must not mutate
		// the parent process's PROXYDOCK_VERSION.
		command = "env"
		args = append([]string{"PROXYDOCK_VERSION=" + opts.Version, "bash"}, args...)
	}
	if err := runner.Run(ctx, u.Stdin, stdout, stderr, command, args...); err != nil {
		return fmt.Errorf("执行 ProxyDock %s脚本: %w", operation, err)
	}
	return nil
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, input io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, stdout, stderr
	// Send cancellation to the installer and its download children.
	// SIGTERM lets the shell remove its temporary files before exiting.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err == syscall.ESRCH {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
