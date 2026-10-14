package cli

import (
	"os"

	"github.com/spf13/cobra"

	"proxydock/internal/docker"
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
	root := docker.New(opts).Command()
	root.Version = version
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.SetIn(os.Stdin)
	if opts.Out != nil {
		root.SetOut(opts.Out)
	}
	if opts.ErrOut != nil {
		root.SetErr(opts.ErrOut)
	}
	return root
}
