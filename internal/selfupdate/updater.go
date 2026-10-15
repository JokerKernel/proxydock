// Package selfupdate runs ProxyDock's embedded program installer.
package selfupdate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"proxydock/scripts"
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
	if opts.Version != "" {
		previous, existed := os.LookupEnv("PROXYDOCK_VERSION")
		if err := os.Setenv("PROXYDOCK_VERSION", opts.Version); err != nil {
			return fmt.Errorf("准备安装脚本: %w", err)
		}
		defer func() {
			if existed {
				_ = os.Setenv("PROXYDOCK_VERSION", previous)
				return
			}
			_ = os.Unsetenv("PROXYDOCK_VERSION")
		}()
	}
	file, err := os.CreateTemp("", "proxydock-management-*.sh")
	if err != nil {
		return fmt.Errorf("准备安装脚本: %w", err)
	}
	defer os.Remove(file.Name())
	_, err = io.WriteString(file, scripts.Installer)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写入安装脚本: %w", err)
	}
	args := []string{file.Name()}
	action := "update"
	if opts.Uninstall {
		action = "uninstall"
		args = append(args, "uninstall")
	}
	if opts.AssumeYes {
		args = append(args, "--yes")
	}
	runner := u.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	stdout, stderr := u.Stdout, u.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = stdout
	}
	if err := runner.Run(ctx, u.Stdin, stdout, stderr, "bash", args...); err != nil {
		return fmt.Errorf("执行 ProxyDock 程序 %s 失败: %w", action, err)
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
