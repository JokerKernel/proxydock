package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"proxydock/internal/terminal"
)

const (
	phaseCheck  = terminal.PhaseCheck
	phaseBackup = terminal.PhaseBackup
	phaseApply  = terminal.PhaseApply
	phaseResult = terminal.PhaseResult

	levelInfo   = terminal.LevelInfo
	levelStep   = terminal.LevelStep
	levelResult = terminal.LevelResult
	levelWarn   = terminal.LevelWarn
	levelError  = terminal.LevelError
)

// opLabel names one Docker invocation. An empty name records only the debug line.
type opLabel struct {
	phase int
	level terminal.Level
	name  string
}

func (m *Manager) op(phase int, level terminal.Level, name, detail string) {
	if m.errOut == nil {
		return
	}
	if phase != 0 && m.opPhase != 0 && phase != m.opPhase {
		fmt.Fprintln(m.errOut)
	}
	if phase != 0 {
		m.opPhase = phase
	}
	terminal.WriteOperation(m.errOut, level, name, detail)
}

func (m *Manager) breakOp() {
	if m.opPhase != 0 && m.errOut != nil {
		fmt.Fprintln(m.errOut)
	}
	m.opPhase = 0
}

func (m *Manager) dockerOp(ctx context.Context, label opLabel, args ...string) ([]byte, error) {
	full := append([]string{"--host", endpoint}, args...)
	m.logDockerCommand(label, full)
	return m.runner.Run(ctx, "docker", full...)
}

func (m *Manager) dockerStreamOp(ctx context.Context, label opLabel, out, errOut io.Writer, args ...string) error {
	full := append([]string{"--host", endpoint}, args...)
	m.logDockerCommand(label, full)
	return m.runner.RunStreaming(ctx, out, errOut, "docker", full...)
}

func (m *Manager) composeOp(ctx context.Context, label opLabel, args ...string) ([]byte, error) {
	prefix := []string{"compose", "--project-name", Project, "--project-directory", m.paths.Deploy(), "--file", m.paths.Compose()}
	return m.dockerOp(ctx, label, append(prefix, args...)...)
}

func (m *Manager) logDockerCommand(label opLabel, full []string) {
	if label.name != "" {
		m.op(label.phase, label.level, label.name, briefDockerCommand(full))
	}
	if !m.traceDocker || m.quietDockerNotes || m.errOut == nil {
		return
	}
	terminal.WriteOperation(m.errOut, levelInfo, "命令详情", "docker "+formatCommandArgs(full))
}

func (m *Manager) logBackup(dir string, snapshots []fileSnapshot) {
	var paths []string
	for _, snapshot := range snapshots {
		if snapshot.Exists {
			paths = append(paths, snapshot.Path)
		}
	}
	if len(paths) > 0 {
		m.op(phaseBackup, levelStep, "备份原配置", strings.Join(paths, "\n"))
	}
	m.op(phaseBackup, levelInfo, "备份位置", dir)
}

func formatCommandArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteCommandArg(arg)
	}
	return strings.Join(quoted, " ")
}

func quoteCommandArg(arg string) string {
	if arg == "" || strings.ContainsAny(arg, " \t\n\"'\\$`") {
		return strconv.Quote(arg)
	}
	return arg
}

func briefDockerCommand(args []string) string {
	rest, _ := dropOpt(args, "--host")
	if len(rest) == 0 {
		return "docker"
	}
	switch rest[0] {
	case "compose":
		dropped := false
		for _, flag := range []string{"--project-name", "--project-directory", "--file"} {
			var one bool
			rest, one = dropOpt(rest, flag)
			dropped = dropped || one
		}
		if len(rest) == 0 || rest[0] != "compose" {
			return strings.TrimSpace("docker " + strings.Join(rest, " "))
		}
		tail := strings.Join(rest[1:], " ")
		if dropped {
			return strings.TrimSpace("docker compose … " + tail)
		}
		return strings.TrimSpace("docker compose " + tail)
	case "run":
		return briefRun(rest)
	case "image":
		if len(rest) >= 3 && (rest[1] == "pull" || rest[1] == "inspect") {
			return "docker image " + rest[1] + " " + rest[len(rest)-1]
		}
	case "container":
		return briefContainer(rest)
	case "logs":
		return "docker logs …"
	case "info":
		return "docker info …"
	}
	return strings.TrimSpace("docker " + strings.Join(rest, " "))
}

func briefRun(args []string) string {
	i := 1
	for i < len(args) {
		arg := args[i]
		if arg == "--" {
			i++
			break
		}
		if strings.HasPrefix(arg, "-") {
			if strings.Contains(arg, "=") || !flagTakesValue(arg) {
				i++
				continue
			}
			if i+1 < len(args) {
				i += 2
			} else {
				i++
			}
			continue
		}
		i++
		break
	}
	var kept []string
	for i < len(args) {
		arg := args[i]
		switch arg {
		case "-config", "-confdir", "-format":
			kept = append(kept, arg, "…")
			if i+1 < len(args) {
				i += 2
			} else {
				i++
			}
		default:
			if strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "sha256:") {
				kept = append(kept, "…")
			} else {
				kept = append(kept, arg)
			}
			i++
		}
	}
	return strings.TrimSpace("docker run … " + strings.Join(kept, " "))
}

func briefContainer(rest []string) string {
	if len(rest) < 2 {
		return "docker container"
	}
	action := rest[1]
	tail := rest[2:]
	id, flags := splitPositionals(tail)
	switch action {
	case "ls":
		if len(tail) == 0 {
			return "docker container ls"
		}
		return "docker container ls …"
	case "inspect":
		if flags || id == "" {
			return "docker container inspect …"
		}
		return "docker container inspect " + id
	case "stop", "rm":
		if flags {
			if id == "" {
				return "docker container " + action + " …"
			}
			return "docker container " + action + " … " + id
		}
		if id == "" {
			return "docker container " + action
		}
		return "docker container " + action + " " + id
	default:
		return "docker container " + strings.Join(rest[1:], " ")
	}
}

func splitPositionals(tail []string) (string, bool) {
	id := ""
	flags := false
	for _, arg := range tail {
		if strings.HasPrefix(arg, "-") {
			flags = true
			continue
		}
		id = arg
	}
	return id, flags
}

func dropOpt(args []string, name string) ([]string, bool) {
	out := make([]string, 0, len(args))
	dropped := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == name || strings.HasPrefix(arg, name+"=") {
			dropped = true
			if !strings.Contains(arg, "=") && flagTakesValue(name) && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out, dropped
}

func flagTakesValue(name string) bool {
	switch name {
	case "--rm", "--read-only", "--quiet", "--no-start", "--force-recreate", "--no-deps", "-d", "--detach", "--all", "--no-trunc", "--follow", "--force", "--volumes", "--short", "--status", "-t", "-i", "-q":
		return false
	default:
		return true
	}
}

func healthStartDetail(ports []int) string {
	if len(ports) == 0 {
		return "监听端口"
	}
	return "端口 " + formatPorts(ports)
}
