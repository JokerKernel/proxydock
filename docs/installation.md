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
bash /tmp/proxydock-install.sh --help
sudo bash /tmp/proxydock-install.sh
```

安装脚本需要 Bash、curl 或 wget、flock 及常见 coreutils 命令。只安装或替换 ProxyDock 程序；不安装 Docker、不启动或更新容器，也不改节点配置。

## 固定版本、重装与降级

```bash
# VERSION 替换为本仓库已有的发布标签，例如 v1.0.0
sudo bash /tmp/proxydock-install.sh --version VERSION

# 同版本重新安装
sudo bash /tmp/proxydock-install.sh --version VERSION --force

# 显式允许安装低于当前程序的版本
sudo bash /tmp/proxydock-install.sh --version VERSION --allow-downgrade
```

版本使用带 `v` 前缀的发布标签。同版本默认跳过，`--force` 可重装；降级必须提供 `--allow-downgrade`。

`PROXYDOCK_VERSION` 可设置默认版本；命令行 `--version` 优先于该环境变量。程序版本与 Xray 镜像版本独立。

## 更新管理程序

```bash
sudo proxydock update
sudo proxydock update --version VERSION
sudo proxydock update --version VERSION --force
sudo proxydock update --version VERSION --allow-downgrade
```

`update` 使用随当前程序内置的安装脚本；不在运行时下载并执行 main 分支的新脚本。更新流程成功后，重新运行 `proxydock` 使用新程序。

默认安装目标为 `/usr/local/sbin/proxydock`，即使从源码目录运行 `./proxydock update`，也更新这个目标。自定义安装位置需要在后续更新和卸载时提供同一个 `PROXYDOCK_INSTALL_DIR`。

`update` 更新管理程序，`upgrade` 升级 Xray 镜像。程序更新和卸载均保留容器、镜像和实例数据。

安装和更新会校验 SHA-256 与程序版本，使用安装锁防止并发操作，再原子替换程序；失败时恢复原程序。

## 卸载管理程序

```bash
sudo proxydock uninstall
sudo proxydock uninstall --yes

# 不依赖已安装程序，也不访问网络
sudo bash /tmp/proxydock-install.sh uninstall --yes
```

交互卸载需要确认，非交互卸载必须提供 `--yes`。卸载只删除安装目标中的 ProxyDock 二进制；已卸载时再次执行会直接返回。

若需要移除 Docker 实例及数据，先使用 `proxydock remove --yes`，再按需执行 `proxydock cleanup --yes`，最后卸载程序。

## 自定义来源和目录

```bash
sudo bash /tmp/proxydock-install.sh --repository OWNER/REPO --version VERSION
sudo PROXYDOCK_INSTALL_DIR=/opt/tools bash /tmp/proxydock-install.sh
sudo PROXYDOCK_INSTALL_DIR=/opt/tools /opt/tools/proxydock update
sudo PROXYDOCK_INSTALL_DIR=/opt/tools /opt/tools/proxydock uninstall --yes
```

`--repository` 优先于 `PROXYDOCK_REPOSITORY`，替代仓库须提供相同的发布文件格式。安装目录必须为绝对路径，已有安装目标须为普通文件。

## 从源码安装

需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
sudo install -m 0755 proxydock /usr/local/sbin/proxydock
```

源码构建默认生成不依赖 libc 的单二进制；`CGO_ENABLED` 可显式覆盖。构建、测试与发布见[开发指南](development.md)。

[返回项目首页](../README.md)
