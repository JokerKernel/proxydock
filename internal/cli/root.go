package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"proxydock/internal/docker"
	"proxydock/internal/selfupdate"
	"proxydock/internal/terminal"
)

// New creates the standalone Docker manager without native kernel dependencies.
func New(version string) *cobra.Command {
	return newCommand(version, docker.Options{
		Root:   os.Getenv("PROXYDOCK_ROOT"),
		Out:    terminal.NewTerminalColorWriter(os.Stdout),
		ErrOut: terminal.NewTerminalColorWriter(os.Stderr),
	})
}

func newCommand(version string, opts docker.Options) *cobra.Command {
	return newCommandWithUpdater(version, opts, nil)
}

func newCommandWithUpdater(version string, opts docker.Options, update func(context.Context, selfupdate.Options) error) *cobra.Command {
	var root *cobra.Command
	if update == nil {
		update = func(ctx context.Context, options selfupdate.Options) error {
			return (selfupdate.Updater{
				Stdin: root.InOrStdin(), Stdout: root.OutOrStdout(), Stderr: root.ErrOrStderr(),
			}).Run(ctx, options)
		}
	}
	root = docker.New(opts).Command()
	root.Version = version
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.SetIn(os.Stdin)
	if opts.Out != nil {
		root.SetOut(opts.Out)
	}
	if opts.ErrOut != nil {
		root.SetErr(opts.ErrOut)
	}
	root.AddCommand(managementCommand("update", update), managementCommand("uninstall", update))
	return root
}

func managementCommand(action string, update func(context.Context, selfupdate.Options) error) *cobra.Command {
	var opts selfupdate.Options
	opts.Uninstall = action == "uninstall"
	short := "更新 ProxyDock 程序，保留容器、镜像和节点数据"
	if opts.Uninstall {
		short = "卸载 ProxyDock 程序，保留容器、镜像和节点数据"
	}
	cmd := &cobra.Command{
		Use: action, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.AssumeYes, _ = cmd.Root().PersistentFlags().GetBool("yes")
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return update(ctx, opts)
		},
	}
	if !opts.Uninstall {
		cmd.Flags().StringVar(&opts.Version, "version", "", "固定 ProxyDock 程序版本，例如 v1.0.0；缺省 latest")
	}
	return cmd
}
