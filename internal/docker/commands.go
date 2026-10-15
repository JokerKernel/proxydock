package docker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"proxydock/internal/docker/model"
	"proxydock/internal/terminal"
)

func (m *Manager) Command() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{Use: "proxydock", Short: "独立管理 Xray Docker Compose 实例", SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		m.out = cmd.OutOrStdout()
		m.errOut = cmd.ErrOrStderr()
		return m.rootCheck()
	}, RunE: func(cmd *cobra.Command, _ []string) error { return m.Menu(cmd.Context(), cmd.InOrStdin()) }}
	cmd.PersistentFlags().BoolVarP(&yes, "yes", "y", false, "确认配置覆盖、容器移除、残留清理及程序卸载")
	cmd.PersistentFlags().BoolVar(&m.traceDocker, "debug", false, "显示完整命令参数")
	for _, action := range []string{"prepare", "upgrade"} {
		action := action
		var version, network, runUser string
		sub := &cobra.Command{Use: action, Short: map[string]string{"prepare": "准备官方镜像和 Compose，不启动代理", "upgrade": "升级镜像，保留凭据并支持回滚"}[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if action == "prepare" {
				if strings.TrimSpace(network) == "" {
					network = defaultNetwork
				}
				root := false
				if strings.TrimSpace(runUser) != "" {
					var err error
					root, err = parseRunUser(runUser)
					if err != nil {
						return err
					}
				}
				return m.prepareWithNetwork(cmd.Context(), version, network, root)
			}
			return m.upgrade(cmd.Context(), version, network, runUser)
		}}
		sub.Flags().StringVar(&version, "version", "", "latest（缺省，官方 latest 标签）、pre-release 或具体版本号")
		networkHelp := "网络方式：publish 或 host，缺省保持当前"
		userHelp := "运行用户：default 或 root，缺省保持当前"
		if action == "prepare" {
			networkHelp = "网络方式：publish（缺省，端口映射）或 host（宿主机网络）"
			userHelp = "运行用户：default（缺省，镜像默认）或 root"
		}
		sub.Flags().StringVar(&network, "network", "", networkHelp)
		sub.Flags().StringVar(&runUser, "user", "", userHelp)
		cmd.AddCommand(sub)
	}
	deploy := m.generateCommand("deploy", &yes)
	var version, network, runUser string
	deploy.Flags().StringVar(&version, "version", "", "首次部署镜像：latest（缺省）、pre-release 或具体版本号")
	deploy.Flags().StringVar(&network, "network", "", "网络方式：publish（首次缺省，端口映射）或 host（宿主机网络），已有实例沿用当前")
	deploy.Flags().StringVar(&runUser, "user", "", "运行用户：default（首次缺省，镜像默认）或 root，已有实例沿用当前")
	cmd.AddCommand(deploy)
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "查看 Docker 实例状态", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := m.Status(cmd.Context())
		b, e := jsonBytes(s)
		if e != nil {
			return e
		}
		fmt.Fprint(m.out, string(b))
		return err
	}})
	for _, action := range []string{"remove", "cleanup", "recover"} {
		action := action
		cmd.AddCommand(&cobra.Command{Use: action, Short: map[string]string{"remove": "移除容器，保留配置和数据", "cleanup": "停止容器并删除本模块配置和数据，保留容器", "recover": "恢复未完成事务"}[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if action != "recover" {
				if err := m.confirmInput(cmd.InOrStdin(), yes, "确认执行 Docker "+action+"？"); err != nil {
					return err
				}
			}
			switch action {
			case "remove":
				return m.Remove(cmd.Context())
			case "cleanup":
				return m.Cleanup(cmd.Context())
			default:
				return m.Recover(cmd.Context())
			}
		}})
	}
	service := &cobra.Command{Use: "service <start|stop|restart|status|logs>", Short: "管理 Docker Xray 服务", Args: cobra.ExactArgs(1)}
	var follow bool
	service.Flags().BoolVarP(&follow, "follow", "f", false, "持续查看日志")
	service.RunE = func(cmd *cobra.Command, args []string) error {
		if follow && args[0] != "logs" {
			return fmt.Errorf("--follow 仅用于日志")
		}
		if args[0] == "status" {
			s, err := m.Status(cmd.Context())
			b, _ := jsonBytes(s)
			fmt.Fprint(m.out, string(b))
			return err
		}
		if follow {
			return m.FollowLogs(cmd.Context())
		}
		b, err := m.Service(cmd.Context(), args[0])
		fmt.Fprint(m.out, string(b))
		return err
	}
	cmd.AddCommand(service)
	config := &cobra.Command{Use: "config", Short: "独立管理 Docker 服务端和客户端配置"}
	config.AddCommand(m.generateCommand("generate", &yes))
	config.AddCommand(&cobra.Command{Use: "show", Short: "显示含敏感凭据的服务端配置", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		b, err := m.ServerConfig()
		if err != nil {
			return err
		}
		terminal.WriteOperation(m.errOut, terminal.LevelWarn, "配置内容", "包含 UUID 和私钥，请妥善保管")
		fmt.Fprint(m.out, string(b))
		return nil
	}})
	config.AddCommand(&cobra.Command{Use: "validate", Short: "使用固定镜像校验正式配置", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return m.Validate(cmd.Context()) }})
	config.AddCommand(&cobra.Command{Use: "edit", Short: "编辑配置副本并事务应用", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return m.Edit(cmd.Context(), cmd.InOrStdin()) }})
	settings := &cobra.Command{Use: "set <dns|log|outbound-ip|fallback-ip|network|user> <value>", Short: "修改 DNS、日志、地址族、网络方式或运行用户", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "network" {
			return m.SetNetwork(cmd.Context(), args[1])
		}
		if args[0] == "user" {
			root, err := parseRunUser(args[1])
			if err != nil {
				return err
			}
			return m.SetRunAsRoot(cmd.Context(), root)
		}
		return m.PatchSetting(cmd.Context(), args[0], args[1])
	}}
	config.AddCommand(settings)
	var reset model.ResetOptions
	resetCmd := &cobra.Command{Use: "reset", Short: "轮换主节点凭据，按需轮换 TLS 证书", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := m.confirmInput(cmd.InOrStdin(), yes, "重置凭据会使原客户端失效，确认继续？"); err != nil {
			return err
		}
		reset.Credentials = reset.Credentials || !reset.RotateCertificate
		return m.Reset(cmd.Context(), reset)
	}}
	resetCmd.Flags().BoolVar(&reset.RotateCertificate, "rotate-certificate", false, "轮换自签 TLS 证书")
	resetCmd.Flags().BoolVar(&reset.Credentials, "credentials", false, "轮换证书时同时轮换 UUID；普通 reset 默认轮换凭据")
	resetCmd.Flags().StringVar(&reset.SNI, "sni", "", "更新 REALITY SNI")
	resetCmd.Flags().StringVar(&reset.Target, "target", "", "更新 REALITY target")
	config.AddCommand(resetCmd)
	var format, name, path string
	var force bool
	client := &cobra.Command{Use: "client", Short: "导出普通、落地或中转客户端配置", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		b, err := m.Client(cmd.Context(), format, name)
		if err != nil {
			return err
		}
		if path != "" {
			return m.WriteExport(path, b, force)
		}
		fmt.Fprint(m.out, string(b))
		return nil
	}}
	client.Flags().StringVar(&format, "format", "native", "native 或 clash")
	client.Flags().StringVar(&name, "name", "", "落地/中转线路名称，空值为主节点")
	client.Flags().StringVar(&path, "output", "", "导出文件，空值输出到 stdout")
	client.Flags().BoolVar(&force, "force", false, "覆盖导出文件")
	config.AddCommand(client, m.accessCommand(), m.landingCommand(), m.relayCommand())
	cmd.AddCommand(config)
	bindCancellation(cmd)
	return cmd
}

func bindCancellation(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		bindCancellation(child)
	}
	if cmd.RunE == nil {
		return
	}
	run := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		parent := cmd.Context()
		ctx, stop := notifyCommand(parent)
		defer stop()
		defer cmd.SetContext(parent)
		cmd.SetContext(ctx)
		return run(cmd, args)
	}
}
func (m *Manager) generateCommand(use string, yes *bool) *cobra.Command {
	var opts model.GenerateOptions
	var force bool
	cmd := &cobra.Command{Use: use, Short: "生成 REALITY/TLS 配置，保留现有凭据与线路", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateGenerateInput(opts); err != nil {
			return err
		}
		if _, err := os.Stat(m.paths.Config()); err == nil && !force {
			if err = m.confirmInput(cmd.InOrStdin(), *yes, "完整生成会覆盖手动配置，确认继续？"); err != nil {
				return err
			}
		}
		if opts.Server == "" && terminalInput(cmd.InOrStdin()) {
			var err error
			opts, err = m.askGenerate(cmd.InOrStdin(), opts)
			if err != nil {
				return err
			}
		}
		if use == "deploy" {
			version, _ := cmd.Flags().GetString("version")
			network, _ := cmd.Flags().GetString("network")
			runUser, _ := cmd.Flags().GetString("user")
			if err := m.prepareDeployment(cmd.Context(), version, network, runUser); err != nil {
				return err
			}
		}
		return m.Generate(cmd.Context(), opts, use == "deploy")
	}}
	flags := cmd.Flags()
	flags.StringVar(&opts.Security, "security", "", "reality 或 tls，缺省沿用当前协议")
	flags.StringVar(&opts.Server, "server", "", "对外服务器地址")
	flags.IntVar(&opts.Port, "port", 0, "监听端口，首次缺省 443")
	flags.StringVar(&opts.SNI, "sni", "", "REALITY SNI 域名")
	flags.StringVar(&opts.Target, "target", "", "REALITY 回落目标 host:port")
	flags.StringVar(&opts.UserName, "user-name", "", "主节点用户名")
	flags.StringVar(&opts.InboundTag, "inbound-tag", "", "入站标签")
	flags.BoolVar(&opts.StandardConfig, "standard-config", false, "关闭默认回落防护")
	flags.IntVar(&opts.XrayFallbackPort, "fallback-port", 0, "回落防护内部端口")
	flags.BoolVar(&opts.XrayFallbackHTTPDomain, "fallback-http-domain", false, "限制回落 HTTP Host")
	flags.BoolVar(&opts.XrayFallbackExactDomain, "fallback-exact-domain", false, "严格匹配回落域名")
	flags.BoolVar(&opts.RotateCredentials, "rotate-credentials", false, "轮换 UUID 和 REALITY 密钥")
	flags.BoolVar(&opts.RotateCertificate, "rotate-certificate", false, "轮换 TLS 证书")
	flags.BoolVar(&opts.DropLinks, "drop-links", false, "丢弃已有落地/中转线路")
	flags.BoolVar(&force, "force", false, "确认完整覆盖服务端配置")
	return cmd
}
func (m *Manager) accessCommand() *cobra.Command {
	var exact bool
	cmd := &cobra.Command{Use: "access <list|mode|switch|add|remove|clear> [args]", Short: "白名单、黑名单和访问模式", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "list" {
			if len(args) != 1 {
				return fmt.Errorf("list 不接受参数")
			}
			s, err := m.readManagedState(cmd.Context())
			if err != nil {
				return err
			}
			b, _ := jsonBytes(s.Node.AccessPolicy)
			fmt.Fprint(m.out, string(b))
			return nil
		}
		change, err := accessPolicyChange(args, exact)
		if err != nil {
			return err
		}
		return m.updatePolicy(cmd.Context(), change)
	}}
	cmd.Flags().BoolVar(&exact, "exact", false, "域名精确匹配")
	return cmd
}

// Validate arguments before locking or recovering, then apply them to fresh state.
func accessPolicyChange(args []string, exact bool) (func(model.AccessPolicy) (model.AccessPolicy, error), error) {
	switch args[0] {
	case "mode":
		if len(args) != 2 || args[1] != model.AccessModeDefault && args[1] != model.AccessModeWhitelist {
			return nil, fmt.Errorf("mode <default|whitelist>")
		}
		return func(p model.AccessPolicy) (model.AccessPolicy, error) { p.Mode = args[1]; return p, nil }, nil
	case "switch":
		if len(args) != 2 || args[1] != "on" && args[1] != "off" {
			return nil, fmt.Errorf("switch <on|off>")
		}
		return func(p model.AccessPolicy) (model.AccessPolicy, error) { p.Disabled = args[1] == "off"; return p, nil }, nil
	case "clear":
		if len(args) != 2 || args[1] != "white" && args[1] != "black" {
			return nil, fmt.Errorf("clear <white|black>")
		}
		return func(p model.AccessPolicy) (model.AccessPolicy, error) { return p.ClearList(args[1]) }, nil
	case "add", "remove":
		if len(args) < 3 || args[1] != "white" && args[1] != "black" {
			return nil, fmt.Errorf("add/remove <white|black> <域名/IP/CIDR...>")
		}
		entries, err := model.ParseAccessEntries(strings.Join(args[2:], " "), exact)
		if err != nil {
			return nil, err
		}
		return func(p model.AccessPolicy) (model.AccessPolicy, error) {
			list := &p.Whitelist
			if args[1] == "black" {
				list = &p.Blacklist
			}
			for _, entry := range entries {
				if args[0] == "add" {
					*list = append(*list, entry)
				} else {
					filtered := (*list)[:0]
					for _, value := range *list {
						if value != entry {
							filtered = append(filtered, value)
						}
					}
					*list = filtered
				}
			}
			return p, nil
		}, nil
	default:
		return nil, fmt.Errorf("访问策略操作无效")
	}
}
func (m *Manager) landingCommand() *cobra.Command {
	var security string
	var port int
	var output string
	cmd := &cobra.Command{Use: "landing <list|add|peer|enable|disable|remove> [name]", Short: "管理落地接入", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "list" {
			if len(args) != 1 {
				return fmt.Errorf("list 不接受名称")
			}
			s, err := m.readManagedState(cmd.Context())
			if err != nil {
				return err
			}
			b, _ := jsonBytes(s.Node.LandingAccesses)
			fmt.Fprint(m.out, string(b))
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("需要接入名称")
		}
		var peer model.LandingPeer
		var err error
		switch args[0] {
		case "add":
			peer, err = m.AddLanding(cmd.Context(), args[1], security, port)
		case "peer":
			peer, err = m.peer(cmd.Context(), args[1])
		case "enable", "disable", "remove":
			return m.ChangeLink(cmd.Context(), "landing", args[1], args[0])
		default:
			return fmt.Errorf("落地操作无效")
		}
		if err != nil {
			return err
		}
		b, _ := jsonBytes(peer)
		if output != "" {
			return m.WriteExport(output, b, false)
		}
		fmt.Fprint(m.out, string(b))
		return nil
	}}
	cmd.Flags().StringVar(&security, "security", "reality", "current（共享主节点端口）、reality 或 tls（独立端口）")
	cmd.Flags().IntVar(&port, "port", 0, "独立 TLS 接入端口")
	cmd.Flags().StringVar(&output, "output", "", "peer JSON 导出路径")
	return cmd
}
func (m *Manager) relayCommand() *cobra.Command {
	var peerFile string
	cmd := &cobra.Command{Use: "relay <list|add|enable|disable|remove> [name]", Short: "管理中转线路", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "list" {
			if len(args) != 1 {
				return fmt.Errorf("list 不接受名称")
			}
			s, err := m.readManagedState(cmd.Context())
			if err != nil {
				return err
			}
			b, _ := jsonBytes(s.Node.RelayLinks)
			fmt.Fprint(m.out, string(b))
			return nil
		}
		if len(args) != 2 {
			return fmt.Errorf("需要线路名称")
		}
		switch args[0] {
		case "add":
			if peerFile == "" {
				return fmt.Errorf("需要 --peer-file")
			}
			absolute, err := filepath.Abs(peerFile)
			if err != nil {
				return err
			}
			var peer model.LandingPeer
			if err = loadJSON(absolute, &peer); err != nil {
				return err
			}
			return m.AddRelay(cmd.Context(), args[1], peer)
		case "enable", "disable", "remove":
			return m.ChangeLink(cmd.Context(), "relay", args[1], args[0])
		default:
			return fmt.Errorf("中转操作无效")
		}
	}}
	cmd.Flags().StringVar(&peerFile, "peer-file", "", "落地接入导出的 0600 peer JSON")
	return cmd
}
