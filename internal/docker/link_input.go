package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"proxydock/internal/docker/model"
	"proxydock/internal/terminal"
)

func (m *Manager) askLandingSource(ctx context.Context, u *ui) (model.LandingPeer, error) {
	u.menuChoice("1", "打开临时编辑文件并粘贴连接文本（推荐）")
	u.menuChoice("2", "手动输入落地连接信息")
	u.menuChoice("3", "读取已有落地连接 JSON 文件")
	u.menuChoice("0/q", "返回")
	choice, err := u.chooseNumberCancelable("请选择落地信息来源", 1, 3, 1)
	if err != nil {
		return model.LandingPeer{}, err
	}
	switch choice {
	case 1:
		return m.pasteLandingText(ctx, u)
	case 2:
		return askLandingPeer(u)
	default:
		path, err := u.ask("落地连接 JSON 文件（0600 权限）", "")
		if err != nil {
			return model.LandingPeer{}, err
		}
		return readLandingInput(path, false, nil)
	}
}

func (m *Manager) pasteLandingText(ctx context.Context, u *ui) (model.LandingPeer, error) {
	if !u.interactive && m.runEditor == nil {
		return model.LandingPeer{}, fmt.Errorf("粘贴编辑器需要交互终端；命令行请使用 --upstream-stdin")
	}
	editor := ""
	for _, name := range []string{"vim", "nano", "vi"} {
		if path, err := m.lookPath(name); err == nil {
			editor = path
			break
		}
	}
	if editor == "" {
		return model.LandingPeer{}, fmt.Errorf("未找到 vim/nano/vi，可改用手动输入或文件导入")
	}
	if err := privateDir(m.paths.Staging()); err != nil {
		return model.LandingPeer{}, err
	}
	if err := m.withLock(ctx, func() error {
		if err := m.checkOwner(); err != nil {
			return err
		}
		return m.cleanupLandingPasteDirs()
	}); err != nil {
		return model.LandingPeer{}, err
	}
	dir, err := os.MkdirTemp(m.paths.Staging(), "landing-paste-")
	if err != nil {
		return model.LandingPeer{}, err
	}
	lease, err := os.Open(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return model.LandingPeer{}, err
	}
	defer lease.Close()
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			m.op(phaseResult, levelWarn, "临时目录清理失败", dir+": "+err.Error())
		}
	}()
	if err := syscall.Flock(int(lease.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return model.LandingPeer{}, err
	}
	if err := saveJSON(filepath.Join(dir, ".proxydock-landing.json"), ownership{SchemaVersion: 1, ManagedBy: "proxydock", InstanceID: Instance}); err != nil {
		return model.LandingPeer{}, err
	}
	path := filepath.Join(dir, "landing.json")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		return model.LandingPeer{}, err
	}
	fmt.Fprintf(u.out, "将打开 %s，请粘贴落地服务器生成的完整 JSON，保存并退出。\n", filepath.Base(editor))
	if m.runEditor != nil {
		err = m.runEditor(ctx, editor, path)
	} else {
		cmd := exec.CommandContext(ctx, editor, path)
		cmd.Dir = dir
		cmd.ExtraFiles = []*os.File{lease}
		cmd.Stdin = os.Stdin
		cmd.Stdout = terminal.RawWriter(u.out)
		cmd.Stderr = terminal.RawWriter(m.errOut)
		err = cmd.Run()
	}
	if err != nil {
		return model.LandingPeer{}, fmt.Errorf("编辑器退出异常: %w", err)
	}
	peer, err := readLandingInput(path, false, nil)
	if err == nil {
		fmt.Fprintln(u.out, "连接文本解析成功，临时文件及编辑器备份将在返回时清理。")
	}
	return peer, err
}

func askLandingPeer(u *ui) (model.LandingPeer, error) {
	peer := model.LandingPeer{Core: model.CoreXray, Flow: model.VisionFlow}
	var err error
	peer.Name, err = u.ask("落地接入名称", "landing")
	if err != nil {
		return peer, err
	}
	u.menuChoice("1", "Xray-core")
	u.menuChoice("2", "sing-box")
	core, err := u.chooseNumberCancelable("请选择落地内核", 1, 2, 1)
	if err != nil {
		return peer, err
	}
	if core == 2 {
		peer.Core = "sing-box"
	}
	u.menuChoice("1", "VLESS + RAW + REALITY + Vision")
	u.menuChoice("2", "VLESS + RAW + TLS + Vision")
	security, err := u.chooseNumberCancelable("请选择落地安全协议", 1, 2, 1)
	if err != nil {
		return peer, err
	}
	peer.Security = model.LandingSecurityReality
	if security == 2 {
		peer.Security = model.LandingSecurityTLS
	}
	peer.Server, err = u.ask("落地 IP 或域名", "")
	if err != nil {
		return peer, err
	}
	rawPort, err := u.ask("落地端口", "443")
	if err != nil {
		return peer, err
	}
	peer.Port, err = strconv.Atoi(rawPort)
	if err != nil {
		return peer, fmt.Errorf("落地端口无效")
	}
	peer.SNI, err = u.ask("落地 SNI / TLS 证书域名", "")
	if err != nil {
		return peer, err
	}
	peer.UUID, err = u.ask("落地接入 UUID", "")
	if err != nil {
		return peer, err
	}
	fields := []struct {
		label string
		value *string
	}{{"落地 REALITY 公钥", &peer.PublicKey}, {"落地 short ID", &peer.ShortID}}
	if peer.Security == model.LandingSecurityTLS {
		fields = []struct {
			label string
			value *string
		}{{"落地证书 SHA-256 指纹（64 位十六进制）", &peer.CertificateSHA256}, {"落地证书公钥 SHA-256 指纹（Base64）", &peer.CertificatePublicKeySHA256}}
	}
	for _, field := range fields {
		*field.value, err = u.ask(field.label, "")
		if err != nil {
			return peer, err
		}
	}
	return peer, validatePeer(peer)
}

func (m *Manager) saveRelayInteractive(ctx context.Context, u *ui, name string, peer model.LandingPeer, update bool) error {
	apply := func(opts RelayOptions) error {
		if update {
			return m.UpdateRelay(ctx, name, peer, opts)
		}
		return m.AddRelay(ctx, name, peer, opts)
	}
	err := apply(RelayOptions{})
	if errors.Is(err, ErrRelayUnreachable) {
		fmt.Fprintln(u.out, err)
		if err := u.confirm("落地当前 TCP 不可达，仍保存线路配置？"); err != nil {
			return err
		}
		err = apply(RelayOptions{AllowUnreachable: true})
	}
	return err
}

func nextLandingName(n model.Node) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf("relay-%d", i)
		if availableLinkName(n, "landing", name) == nil {
			return name
		}
	}
}
