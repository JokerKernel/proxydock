# ProxyDock

ProxyDock 是面向 Linux 的 Docker 代理管理工具，提供中文交互菜单和命令行。目前支持一个独立的 Xray Docker Compose 实例，包括 REALITY/TLS、配置管理、客户端导出、访问控制、中转与落地、镜像升级和失败回滚。

使用 Go 编写，单二进制部署，不需要安装宿主机 Xray 或 systemd 服务。

## 环境

- Linux amd64 / arm64；管理操作需要 root，帮助和版本查询不需要。
- 已安装并启动的本机 rootful Docker Engine，以及 Compose v2 或更高版本插件。
- 默认使用端口映射，也支持 host 网络；监听端口须与其他服务错开。

## 一键安装

安装或更新最新正式版：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh | sudo bash
```

安装脚本从本仓库的 GitHub Release 获取 amd64/arm64 程序，校验 `SHA256SUMS` 和实际版本后原子安装到 `/usr/local/sbin/proxydock`。同版本跳过，默认不降级；安装或更新失败会保留或恢复原程序。Docker Engine 与 Compose 需提前安装。

需要进入管理菜单时再运行 `sudo proxydock`；已是 root 用户时直接运行 `proxydock`。

固定程序版本：

```bash
curl -fsSL https://raw.githubusercontent.com/JokerKernel/proxydock/main/scripts/install.sh | \
  sudo bash -s -- --version v1.0.0
```

版本号应替换为本仓库已发布的版本。完整参数和自定义安装目录见[安装与更新指南](docs/installation.md)。

## 从源码安装

需要 Go 1.23 或更高版本，在仓库根目录运行：

```bash
./scripts/build.sh
sudo install -m 0755 proxydock /usr/local/sbin/proxydock
```

## 常用命令

```bash
proxydock --help
proxydock --version

# 更新管理程序；卸载程序保留容器、镜像和节点数据
sudo proxydock update
sudo proxydock uninstall --yes

# TLS 示例：替换对外地址；使用官方 latest 标签，拉取后固定 digest
sudo proxydock deploy --security tls --server YOUR_SERVER_IP --port 8443

sudo proxydock status
sudo proxydock config client --format clash --output ./client.yaml
sudo proxydock service logs --follow
sudo proxydock upgrade --version VERSION
sudo proxydock recover

# 移除容器保留数据；随后可单独永久清理数据
sudo proxydock remove --yes
sudo proxydock cleanup --yes
```

`update` 更新 ProxyDock 程序，`upgrade` 升级 Xray 镜像。程序更新和卸载通过命令行执行。程序更新使用随二进制内置的安装脚本，安装、校验与回滚行为与一键安装一致。脚本也支持 `sudo bash scripts/install.sh update` 和 `sudo bash scripts/install.sh uninstall --yes`。

## 文档

- [使用指南](docs/usage.md)：部署、配置、服务、升级回滚及文件位置。
- [安装与更新指南](docs/installation.md)：一键安装、固定版本、程序更新与卸载。
- [开发指南](docs/development.md)：模块边界、测试、构建和发布。

运行目录使用 `/opt/proxydock`、`/var/lib/proxydock` 和 `/run/proxydock`，详细文件位置见[使用指南](docs/usage.md#文件位置)。

## 验证

```bash
make check
make cross
```

普通测试使用模拟 Docker，不安装 Docker、不拉取镜像。真实 Docker、官方镜像及代理连接验收需另行在具备环境的 Linux 主机完成。
