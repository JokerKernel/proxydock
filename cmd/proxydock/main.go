package main

import (
	"fmt"
	"os"

	"proxydock/internal/cli"
	"proxydock/internal/terminal"
	"proxydock/internal/version"
)

func main() {
	if err := cli.New(version.String()).Execute(); err != nil {
		fmt.Fprintln(terminal.NewTerminalColorWriter(os.Stderr), "[错误] 操作失败：", err)
		os.Exit(1)
	}
}
