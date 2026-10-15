//go:build linux

package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"proxydock/internal/terminal"
)

func TestEditorInheritsTerminalWithColorOutput(t *testing.T) {
	for _, color := range []bool{false, true} {
		name := "plain"
		if color {
			name = "color"
		}
		t.Run(name, func(t *testing.T) {
			m, _ := deployed(t)
			_, slave := openMenuPTY(t)
			m.out = terminal.NewColorWriter(slave, color)
			m.errOut = terminal.NewColorWriter(slave, color)
			editor := filepath.Join(t.TempDir(), "editor")
			// Check the actual child descriptors: a writer that merely forwards
			// bytes to the terminal still gives the editor a pipe instead.
			if err := os.WriteFile(editor, []byte("#!/bin/sh\n[ -t 1 ] || exit 81\n[ -t 2 ] || exit 82\n"), 0700); err != nil {
				t.Fatal(err)
			}
			lookPath := m.lookPath
			m.lookPath = func(name string) (string, error) {
				if name == "vim" {
					return editor, nil
				}
				return lookPath(name)
			}
			if err := m.Edit(context.Background(), slave); err != nil {
				t.Fatalf("editor did not inherit terminal outputs: %v", err)
			}
		})
	}
}
