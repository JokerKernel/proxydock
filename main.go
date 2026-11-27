package main

import (
	"context"
	"errors"
	"os"

	"proxydock/internal/cli"
	"proxydock/internal/terminal"
	"proxydock/internal/version"
)

func main() {
	if err := cli.New(version.String()).Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		terminal.WriteOperation(terminal.NewTerminalColorWriter(os.Stderr), terminal.LevelError, "操作失败", err.Error())
		os.Exit(1)
	}
}
