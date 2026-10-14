# 安装、更新与卸载 ProxyDock

官方仓库为 [JokerKernel/proxydock](https://github.com/JokerKernel/proxydock)。程序支持 Linux amd64 / arm64；安装和管理操作需要 root。预编译程序不需要 Go，不依赖宿主机 Xray 或 systemd。Docker 代理运行需要已有本机 rootful Docker 与 Compose。

## 一键安装或更新

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh | sudo bash
sudo proxydock
```

脚本默认选择 GitHub 最新正式 Release。仓库需要先发布包含当前架构二进制和 `SHA256SUMS` 的版本。没有可用 Release 时会报错，不会自动编译源码或下载其他项目的程序。

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

默认不会降级，`--force` 只允许同版本重装。版本采用带 `v` 前缀的语义版本，正确比较数字与预发布序号；构建元数据不影响版本顺序。

`PROXYDOCK_VERSION` 可设置默认版本；命令行 `--version` 优先于该环境变量。程序版本与 Xray 镜像版本独立。

## 更新管理程序

```bash
sudo proxydock update
sudo proxydock update --version VERSION
sudo proxydock update --version VERSION --force
sudo proxydock update --version VERSION --allow-downgrade
```

`update` 使用随当前程序内置的安装脚本；不在运行时下载并执行 main 分支的新脚本。可在中文菜单第 7 项执行更新，更新流程成功后退出，重新运行 `proxydock` 使用新程序。

默认安装目标为 `/usr/local/sbin/proxydock`，即使从源码目录运行 `./proxydock update`，也更新这个目标。自定义安装位置需要在后续更新和卸载时提供同一个 `PROXYDOCK_INSTALL_DIR`。

Xray 镜像升级仍使用：

```bash
sudo proxydock upgrade --version VERSION
```

## 校验与失败恢复

脚本先检查系统和权限，再使用安装目录下的 `.proxydock.install.lock` 防止并发安装、更新和卸载。下载优先使用当前代理环境，失败时尝试直连；curl 请求限定 HTTPS 及 HTTPS 重定向，下载设有超时和大小检查。

候选程序必须匹配所选版本与架构的校验清单条目，并通过 SHA-256 和 `--version` 运行检查。校验完成后在安装目录内暂存、备份原程序、原子替换，再次验证已安装程序。失败或收到可处理的中断信号时恢复原程序；首次安装失败则移除未完成的程序。临时下载、候选文件及成功恢复后的备份会清理，安装锁文件保留供后续操作使用。

升级和卸载管理程序不改变容器、镜像、节点凭据、证书、配置及事务记录。

## 卸载管理程序

```bash
sudo proxydock uninstall
sudo proxydock uninstall --yes

# 不依赖已安装程序，也不访问网络
sudo bash /tmp/proxydock-install.sh uninstall --yes
```

交互卸载需要确认，非交互卸载必须提供 `--yes`。菜单第 8 项也可卸载。卸载只删除安装目标中的 ProxyDock 二进制；已卸载时再次执行会直接返回。

若需要移除 Docker 实例及数据，先使用 `proxydock remove --yes`，再按需执行 `proxydock cleanup --yes`，最后卸载程序。

## 自定义来源和目录

```bash
sudo bash /tmp/proxydock-install.sh --repository OWNER/REPO --version VERSION
sudo PROXYDOCK_INSTALL_DIR=/opt/tools bash /tmp/proxydock-install.sh
sudo PROXYDOCK_INSTALL_DIR=/opt/tools /opt/tools/proxydock update
sudo PROXYDOCK_INSTALL_DIR=/opt/tools /opt/tools/proxydock uninstall --yes
```

默认来源固定为 `JokerKernel/proxydock`。`--repository` 优先于 `PROXYDOCK_REPOSITORY`，替代仓库须提供相同的发布文件格式。安装目录必须为绝对路径；安装目标不能是目录、符号链接或其他特殊文件。

## 从源码安装

需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
sudo install -m 0755 proxydock /usr/local/sbin/proxydock
```

源码构建默认生成不依赖 libc 的单二进制；`CGO_ENABLED` 可显式覆盖。构建、测试与发布见[开发指南](development.md)。

[返回项目首页](../README.md)
