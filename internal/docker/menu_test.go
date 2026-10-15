package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Capture both the transcript and the screen remaining after terminal clears.
type dockerTerminalOutput struct {
	bytes.Buffer
	screen string
}

func (*dockerTerminalOutput) IsTerminal() bool { return true }
func (w *dockerTerminalOutput) Write(p []byte) (int, error) {
	w.screen += string(p)
	if at := strings.LastIndex(w.screen, "\033[H\033[2J\033[3J"); at >= 0 {
		w.screen = w.screen[at+len("\033[H\033[2J\033[3J"):]
	}
	return w.Buffer.Write(p)
}

// WriteString must update the screen too. The embedded buffer's WriteString
// would otherwise satisfy io.StringWriter and skip Write.
func (w *dockerTerminalOutput) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// Deliver one answer at a time and capture what the user sees at each prompt.
type dockerTerminalInput struct {
	answers []string
	index   int
	output  *dockerTerminalOutput
	screens []string
}

func (*dockerTerminalInput) IsTerminal() bool { return true }
func (r *dockerTerminalInput) Read(p []byte) (int, error) {
	if r.index == len(r.answers) {
		return 0, io.EOF
	}
	r.screens = append(r.screens, r.output.screen)
	answer := r.answers[r.index]
	n := copy(p, answer)
	if n != len(answer) {
		panic("terminal test answer exceeds reader buffer")
	}
	r.index++
	return n, nil
}

func TestDockerMenusReplacePreviousScreen(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "6\n", "0\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 5 {
		t.Fatalf("unexpected prompts: %d", len(in.screens))
	}
	root := func(screen string) bool {
		return strings.Contains(screen, "部署/升级") && !strings.Contains(screen, "生成/更新配置") && !strings.Contains(screen, "实时日志")
	}
	config := func(screen string) bool {
		return strings.Contains(screen, "生成/更新配置") && !strings.Contains(screen, "实时日志") && !strings.Contains(screen, "部署/升级")
	}
	service := func(screen string) bool {
		return strings.Contains(screen, "实时日志") && !strings.Contains(screen, "生成/更新配置") && strings.Contains(screen, "›  服务端配置  ›  服务管理")
	}
	checks := []func(string) bool{root, config, service, config, root}
	for i, screen := range in.screens {
		if !checks[i](screen) {
			t.Fatalf("prompt %d showed the wrong menu: %s", i, screen)
		}
	}
	if strings.Contains(out.String(), "按 Enter") {
		t.Fatal("submenu navigation should return directly without a pause")
	}
	rootScreen := in.screens[0]
	for _, want := range []string{"准备镜像或升级版本", "生成、修改、查看与服务管理", "4   移除容器", "5   删除实例数据", "6   恢复中断操作"} {
		if !strings.Contains(rootScreen, want) {
			t.Fatalf("root menu missing %q: %s", want, rootScreen)
		}
	}
	if strings.Contains(rootScreen, "4   服务管理") || strings.Contains(rootScreen, "专用运行用户") {
		t.Fatalf("root menu kept a native-only or old item: %s", rootScreen)
	}
	if !strings.Contains(rootScreen, "中转节点配置\n\n  4   移除容器") {
		t.Fatalf("root menu blank line is not between items 3 and 4:\n%s", rootScreen)
	}
	if strings.Contains(rootScreen, "保留配置和数据\n\n  5   删除实例数据") {
		t.Fatalf("root menu still separates item 4 from item 5:\n%s", rootScreen)
	}
	configScreen := in.screens[1]
	for _, want := range []string{"重置节点与 SNI 检测", "6   服务管理", "7   中转与落地配置"} {
		if !strings.Contains(configScreen, want) {
			t.Fatalf("server menu missing %q: %s", want, configScreen)
		}
	}
	if strings.Contains(configScreen, "校验配置") || strings.Contains(configScreen, "专用运行用户") {
		t.Fatalf("server menu kept an item that is not in the Xray-core list: %s", configScreen)
	}
}

func TestRunUserMenuOffersRoot(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n", "5\n", "0\n", "0\n", "2\n", "6\n", "7\n", "0\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) < 8 {
		t.Fatalf("run-user menus were not shown: %d prompts", len(in.screens))
	}
	deploy := in.screens[2]
	service := in.screens[7]
	for _, screen := range []string{deploy, service} {
		for _, want := range []string{"镜像默认", "使用官方镜像自带用户", "root", "以 0:0 启动"} {
			if !strings.Contains(screen, want) {
				t.Fatalf("run-user menu missing %q:\n%s", want, screen)
			}
		}
	}
}

func TestDockerVersionMenuUsesLatestAndPrerelease(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n", "2\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) < 3 {
		t.Fatalf("version menu was not shown: %d prompts", len(in.screens))
	}
	screen := in.screens[2]
	for _, want := range []string{"稳定版", "-- latest", "2   pre-release"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("version menu missing %q: %s", want, screen)
		}
	}
	if strings.Contains(screen, "官方镜像标签") || strings.Contains(screen, "2   latest") {
		t.Fatalf("version menu still lists latest separately: %s", screen)
	}
}

func TestSpecifiedVersionReturnsWithoutChangingSelection(t *testing.T) {
	for _, cancel := range []string{"0", "q", " Q "} {
		t.Run(cancel, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := testManager(t)
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: []string{"1\n", "3\n", "26.3.27\n", "3\n", cancel + "\n", "0\n", "0\n"}, output: &out}
			if err := m.Menu(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != 7 {
				t.Fatalf("return did not reach parent menu: %d prompts\n%s", len(in.screens), out.String())
			}
			if !strings.Contains(in.screens[4], "0/q 返回") || !strings.Contains(in.screens[4], "版本号:") {
				t.Fatalf("version page omitted return instructions:\n%s", in.screens[4])
			}
			if !strings.Contains(in.screens[5], "指定版本 26.3.27") || !strings.Contains(in.screens[5], "1   部署/更新") {
				t.Fatalf("return changed selected version or missed parent menu:\n%s", in.screens[5])
			}
			if strings.Contains(out.String(), "版本格式无效") || strings.Contains(out.String(), "按 Enter") {
				t.Fatalf("return was treated as invalid input or paused:\n%s", out.String())
			}
		})
	}
}

func TestUpgradeWithoutStateKeepsPullLogWithGenerateForm(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n", "0\n"}, output: &out}
	u := newUI(in, &out)
	m.review = u
	err := m.confirmDeploy(context.Background(), u, "", "", false)
	if !errors.Is(err, errCancelled) {
		t.Fatal(err)
	}
	if len(in.screens) != 2 {
		t.Fatalf("prompts = %d\n%s", len(in.screens), out.String())
	}
	form := in.screens[1]
	for _, want := range []string{"拉取镜像", "升级结果", "主节点协议"} {
		if !strings.Contains(form, want) {
			t.Fatalf("generate form dropped %q:\n%s", want, form)
		}
	}
	if strings.Contains(out.String(), "按 Enter") {
		t.Fatalf("upgrade asked for another confirmation:\n%s", out.String())
	}
}

func TestDeployKeepsPrepareLogWithGenerateForm(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n", "0\n"}, output: &out}
	u := newUI(in, &out)
	m.review = u
	err := m.confirmDeploy(context.Background(), u, "", "", false)
	if !errors.Is(err, errCancelled) {
		t.Fatal(err)
	}
	if len(in.screens) != 2 {
		t.Fatalf("prompts = %d\n%s", len(in.screens), out.String())
	}
	form := in.screens[1]
	for _, want := range []string{"拉取镜像", "校验结果", "准备结果", "主节点协议"} {
		if !strings.Contains(form, want) {
			t.Fatalf("generate form dropped %q:\n%s", want, form)
		}
	}
	if strings.Contains(out.String(), "按 Enter") {
		t.Fatalf("deploy asked for another confirmation:\n%s", out.String())
	}
}

func TestDockerModifyMenuMatchesXrayCore(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "3\n", "0\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) < 3 {
		t.Fatalf("modify menu was not shown: %d prompts", len(in.screens))
	}
	screen := in.screens[2]
	for _, want := range []string{"2   重置节点凭证", "3   重置 SNI/target", "4   出站 IP", "REALITY SNI 候选检测", "默认  -- 先 IPv4，300ms 后竞速 IPv6"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("modify menu missing %q: %s", want, screen)
		}
	}
	if strings.Contains(screen, "未设置") {
		t.Fatalf("modify menu still describes the default address policy as unset: %s", screen)
	}
	if strings.Contains(screen, "专用运行用户") {
		t.Fatalf("modify menu included the native service user item: %s", screen)
	}
}

func TestDockerMenuResultsWaitBeforeRedraw(t *testing.T) {
	for _, showConfig := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "config"}[showConfig], func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := testManager(t)
			if showConfig {
				m, _ = deployed(t)
			}
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: []string{"2\n", "2\n", "\n", "0\n", "0\n"}, output: &out}
			if err := m.Menu(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != 5 {
				t.Fatalf("result pause consumed navigation incorrectly: %d prompts", len(in.screens))
			}
			result := in.screens[2]
			if !strings.Contains(result, "按 Enter 返回菜单") || strings.Contains(result, "生成/更新配置") {
				t.Fatalf("result was not shown on its own page: %s", result)
			}
			if showConfig && !strings.Contains(result, "REALITY 私钥") || !showConfig && !strings.Contains(result, "操作失败") {
				t.Fatalf("result disappeared before pause: %s", result)
			}
			if !strings.Contains(in.screens[3], "›  服务端配置") || strings.Contains(in.screens[3], "按 Enter") || strings.Contains(in.screens[3], "操作失败") {
				t.Fatalf("result remained after returning to menu: %s", in.screens[3])
			}
		})
	}
}

func TestDockerMenuPipedInputHasNoTerminalControls(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	if err := m.Menu(context.Background(), strings.NewReader("2\n2\n0\n0\n")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033") || strings.Contains(out.String(), "按 Enter") {
		t.Fatal("piped input must not clear screens or consume pause answers")
	}
}

func TestDockerMenuDumbTerminalHasNoTerminalControls(t *testing.T) {
	t.Setenv("TERM", "dumb")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"2\n", "2\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033") || strings.Contains(out.String(), "按 Enter") {
		t.Fatal("dumb terminal must not receive interactive screen controls")
	}
}
