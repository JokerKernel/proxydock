package docker

import (
	"bytes"
	"context"
	"io"
	"os"
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

func TestLandingPeerRemainsVisibleUntilReturn(t *testing.T) {
	for _, action := range []string{"show", "create"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			ctx := context.Background()
			m, _ := deployed(t)
			answers := []string{"3\n", "1\n", "nihao\n", "\n", "0\n"}
			if action == "show" {
				if _, err := m.AddLanding(ctx, "nihao", "current", 0); err != nil {
					t.Fatal(err)
				}
				answers = []string{"4\n", "1\n", "1\n", "\n", "0\n"}
			}
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: answers, output: &out}
			if err := m.linkMenu(ctx, newUI(in, &out)); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != len(answers) {
				t.Fatalf("connection text was cleared without waiting for Enter: %d prompts, want %d", len(in.screens), len(answers))
			}
			peer, err := m.peer(ctx, "nihao")
			if err != nil {
				t.Fatal(err)
			}
			data, err := landingText(peer)
			if err != nil {
				t.Fatal(err)
			}
			if screen := in.screens[3]; !strings.Contains(screen, string(data)) || !strings.Contains(screen, "按 Enter 返回菜单") {
				t.Fatalf("copyable connection text disappeared before the return prompt: %s", screen)
			}
			if screen := in.screens[4]; !strings.Contains(screen, "中转与落地线路") || strings.Contains(screen, peer.UUID) {
				t.Fatalf("Enter did not return to a clean links menu: %s", screen)
			}
		})
	}
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
		return strings.Contains(screen, "安装") && strings.Contains(screen, "更新") && !strings.Contains(screen, "生成/更新配置") && !strings.Contains(screen, "实时日志")
	}
	config := func(screen string) bool {
		return strings.Contains(screen, "生成/更新配置") && !strings.Contains(screen, "实时日志") && !strings.Contains(screen, "确认安装") && !strings.Contains(screen, "1   安装")
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
	for _, want := range []string{"容器状态  不存在", "安装与更新", "部署容器和更新容器", "生成、修改、查看与服务管理", "4   删除实例数据", "5   恢复中断操作"} {
		if !strings.Contains(rootScreen, want) {
			t.Fatalf("root menu missing %q: %s", want, rootScreen)
		}
	}
	if strings.Contains(rootScreen, "4   服务管理") || strings.Contains(rootScreen, "专用运行用户") || strings.Contains(rootScreen, "4   清理容器") || strings.Contains(rootScreen, "6   恢复中断操作") {
		t.Fatalf("root menu kept a native-only or old item: %s", rootScreen)
	}
	if !strings.Contains(rootScreen, "中转节点配置\n\n  4   删除实例数据") {
		t.Fatalf("root menu blank line is not between items 3 and 4:\n%s", rootScreen)
	}
	if strings.Contains(rootScreen, "证书和备份\n\n  5   恢复中断操作") {
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

func TestDockerUpdateMenuKeepsSavedSelection(t *testing.T) {
	for _, selection := range []string{"latest", "pre-release", "26.3.27"} {
		t.Run(selection, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, fake := testManager(t)
			ctx := context.Background()
			if err := m.Prepare(ctx, selection); err != nil {
				t.Fatal(err)
			}
			fake.calls = nil
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: []string{"1\n", "1\n", "1\n", "\n", "1\n", "0\n", "0\n"}, output: &out}
			if err := m.Menu(ctx, in); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != 7 {
				t.Fatalf("unexpected prompts: %d\n%s", len(in.screens), out.String())
			}
			for _, i := range []int{1, 2, 5} {
				if !strings.Contains(in.screens[i], shownVersion(selection)) {
					t.Fatalf("prompt %d lost saved selection %q:\n%s", i, selection, in.screens[i])
				}
			}
			if count := countDockerCalls(fake, func(c []string) bool {
				return len(c) == 3 && c[0] == "image" && c[1] == "pull" && c[2] == Repository+":"+selection
			}); count != 1 {
				t.Fatalf("update did not pull saved selection %q: %v", selection, fake.calls)
			}
			md, err := m.metadata()
			if err != nil {
				t.Fatal(err)
			}
			if selection == "26.3.27" && md.Channel != "pinned" || selection != "26.3.27" && md.Channel != selection {
				t.Fatalf("update changed saved channel: %+v", md)
			}
		})
	}
}

func TestDockerVersionMenuDefaultsToSavedChannel(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	ctx := context.Background()
	if err := m.Prepare(ctx, "latest"); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n", "2\n", "2\n", "1\n", "1\n", "\n", "1\n", "2\n", "\n", "0\n", "0\n"}, output: &out}
	if err := m.Menu(ctx, in); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 11 {
		t.Fatalf("unexpected prompts: %d\n%s", len(in.screens), out.String())
	}
	if !strings.Contains(in.screens[8], "请选择 [2]") {
		t.Fatalf("reopened version menu did not default to pre-release:\n%s", in.screens[8])
	}
	if !strings.Contains(in.screens[9], "安装渠道  pre-release") {
		t.Fatalf("accepting default changed channel:\n%s", in.screens[9])
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
			if !strings.Contains(in.screens[5], "指定版本 26.3.27") || !strings.Contains(in.screens[5], "1   安装") {
				t.Fatalf("return changed selected version or missed parent menu:\n%s", in.screens[5])
			}
			if strings.Contains(out.String(), "版本格式无效") || strings.Contains(out.String(), "按 Enter") {
				t.Fatalf("return was treated as invalid input or paused:\n%s", out.String())
			}
		})
	}
}

func TestUpgradeDoesNotOpenGenerateForm(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	if err := m.Prepare(context.Background(), "latest"); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n"}, output: &out}
	u := newUI(in, &out)
	m.review = u
	if err := m.confirmDeploy(context.Background(), u, "", "", false); err != nil {
		t.Fatal(err)
	}
	if in.index != 1 {
		t.Fatalf("update asked for more input: %d\n%s", in.index, out.String())
	}
	text := out.String()
	for _, want := range []string{"拉取镜像", "升级结果"} {
		if !strings.Contains(text, want) {
			t.Fatalf("update log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "主节点协议") || strings.Contains(text, "按 Enter") {
		t.Fatalf("update continued into node setup:\n%s", text)
	}
}

func TestPullDoesNotOpenGenerateForm(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	in := &dockerTerminalInput{answers: []string{"1\n"}, output: &out}
	u := newUI(in, &out)
	m.review = u
	if err := m.confirmDeploy(context.Background(), u, "", "", false); err != nil {
		t.Fatal(err)
	}
	if in.index != 1 {
		t.Fatalf("pull asked for more input: %d\n%s", in.index, out.String())
	}
	text := out.String()
	for _, want := range []string{"拉取镜像", "服务端配置", "客户端配置", "准备结果", "下一步"} {
		if !strings.Contains(text, want) {
			t.Fatalf("pull log missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "主节点协议") || strings.Contains(text, "按 Enter") {
		t.Fatalf("pull continued into node setup:\n%s", text)
	}
}

func TestDockerDeployShowsExecutionOnSeparateScreen(t *testing.T) {
	for _, action := range []string{"安装", "更新"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, fake := testManager(t)
			ctx := context.Background()
			if action == "更新" {
				if err := m.Prepare(ctx, "latest"); err != nil {
					t.Fatal(err)
				}
				fake.latestVersion = "26.10.11"
				fake.latestImageID = "sha256:" + strings.Repeat("e", 64)
			}
			fake.calls = nil
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			in := &dockerTerminalInput{answers: []string{"1\n", "1\n", "1\n", "\n", "0\n"}, output: &out}
			if err := m.Menu(ctx, in); err != nil {
				t.Fatal(err)
			}
			if len(in.screens) != 5 {
				t.Fatalf("unexpected prompts: %d\n%s", len(in.screens), out.String())
			}
			screen := in.screens[3]
			for _, want := range []string{action + "过程", "\n\npulled\n\n", "服务端配置", "客户端配置", "下一步", "按 Enter 返回菜单"} {
				if !strings.Contains(screen, want) {
					t.Fatalf("execution screen missing %q:\n%s", want, screen)
				}
			}
			for _, unwanted := range []string{"操作确认", "确认操作", "将执行", "docker image pull", "校验 Xray 配置"} {
				if strings.Contains(screen, unwanted) {
					t.Fatalf("execution screen retained %q:\n%s", unwanted, screen)
				}
			}
			if strings.Count(screen, "pulled\n") != 1 {
				t.Fatalf("native pull output repeated:\n%s", screen)
			}
			if count := countDockerCalls(fake, func(c []string) bool { return c[0] == "run" && contains(c, "-test") }); count != 2 {
				t.Fatalf("expected both capability checks, got %d", count)
			}
		})
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
	// Color follows stdout. This writer is a terminal, so an operation error may
	// contain SGR codes. Those are not screen controls.
	withoutEnv(t, "NO_COLOR")
	withoutEnv(t, "PROXYDOCK_COLOR")
	m, _ := testManager(t)
	var out dockerTerminalOutput
	m.out, m.errOut = &out, &out
	if err := m.Menu(context.Background(), strings.NewReader("2\n2\n0\n0\n")); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, control := range []string{"\033[H", "\033[2J", "\033[1A", "按 Enter"} {
		if strings.Contains(text, control) {
			t.Fatalf("piped input must not clear screens or consume pause answers:\n%s", text)
		}
	}
}

func withoutEnv(t *testing.T, key string) {
	t.Helper()
	value, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !ok {
			os.Unsetenv(key)
			return
		}
		os.Setenv(key, value)
	})
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
