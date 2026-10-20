# 安装、更新与卸载 ProxyDock

程序支持 Linux amd64 / arm64；安装和管理操作需要 root。运行前准备本机 rootful Docker 与 Compose，具体要求见[使用指南](usage.md#环境要求)。

## 一键安装或更新

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh | sudo bash
```

脚本从 [JokerKernel/proxydock](https://github.com/JokerKernel/proxydock) 的最新正式 Release 下载程序，安装到 `/usr/local/sbin/proxydock`。

安装完成后，需要进入管理菜单时再运行 `sudo proxydock`；已是 root 用户时直接运行 `proxydock`。

也可以先下载脚本审阅后执行：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh -o /tmp/proxydock-install.sh
sudo bash /tmp/proxydock-install.sh
```

安装脚本需要 Bash、curl 或 wget，以及 `sha256sum`、`mktemp` 等常见命令。只安装或替换 ProxyDock 程序；不安装 Docker、不启动或更新容器，也不改节点配置。

## 固定版本

```bash
# VERSION 替换为本仓库已有的发布标签，例如 v1.0.0
sudo PROXYDOCK_VERSION=VERSION bash /tmp/proxydock-install.sh
sudo proxydock update --version VERSION
```

版本使用带 `v` 前缀的发布标签。同版本直接跳过。当前程序版本更高时也跳过，避免降级。程序版本与 Xray 镜像版本独立。

查询最新正式版时，先走当前网络或代理访问 GitHub Releases API；失败后再忽略代理直连。仍然失败时改读 Release 里的 `version` 文件。校验清单和程序文件只走当前网络，不会绕过代理。

## 更新管理程序

```bash
sudo proxydock update
sudo proxydock update --version VERSION
```

`update` 使用随当前程序内置的安装脚本；不在运行时下载并执行 main 分支的新脚本。更新流程成功后，重新运行 `proxydock` 使用新程序。

安装目标固定为 `/usr/local/sbin/proxydock`。`update` 更新管理程序，`upgrade` 升级 Xray 镜像。程序更新和卸载均保留容器、镜像和实例数据。

安装界面按版本检查、下载、SHA-256 校验和安装分别显示进度；发布信息与最终结果使用统一卡片，完成后显示版本、安装位置和运行命令。终端下载时显示进度条；重定向输出时使用纯文本，也支持 `NO_COLOR` 和 `PROXYDOCK_COLOR=always/never`。

安装和更新会校验 SHA-256，在安装目录创建随机候选文件，检查候选程序能运行且版本与目标发布一致后，再原子替换程序。下载、校验或候选程序检查失败时保留原程序；安装目标是目录时拒绝覆盖。安装过程隐藏完整构建信息，只在完成卡片中显示程序版本。

## 卸载管理程序

```bash
sudo proxydock uninstall

# 不依赖已安装程序，也不访问网络
sudo bash /tmp/proxydock-install.sh uninstall
```

卸载不需要确认，只删除 `/usr/local/sbin/proxydock`；已卸载时再次执行会直接返回。

若需要移除 Docker 实例及数据，先使用 `proxydock remove --yes`，再按需执行 `proxydock cleanup --yes`，最后卸载程序。

## 从源码安装

需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
sudo install -m 0755 proxydock /usr/local/sbin/proxydock
```

源码构建默认生成不依赖 libc 的单二进制；`CGO_ENABLED` 可显式覆盖。构建、测试与发布见[开发指南](development.md)。

[返回项目首页](../README.md)
