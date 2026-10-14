# ProxyDock

ProxyDock 是面向 Linux 的 Docker 代理管理工具，提供中文交互菜单和命令行。目前支持一个独立的 Xray Docker Compose 实例，包括 REALITY/TLS、配置管理、客户端导出、访问控制、中转与落地、镜像升级和失败回滚。

使用 Go 编写，单二进制部署。Docker 业务从 ProxyForge 分离，在本仓库独立构建、测试和发布，不需要安装宿主机 Xray 或 systemd 服务。

## 环境

- Linux amd64 / arm64；管理操作需要 root，帮助和版本查询不需要。
- 已安装并启动的本机 rootful Docker Engine，以及 Compose v2 或更高版本插件。
- host 网络，端口须与原生代理及其他服务错开。

## 从源码安装

需要 Go 1.23 或更高版本，在仓库根目录运行：

```bash
./scripts/build.sh
sudo install -m 0755 proxydock /usr/local/sbin/proxydock
sudo proxydock
```

发布仓库建立并有 Release 后，也可使用 `sudo bash scripts/install.sh` 安装或更新程序。默认发布来源为 `JokerKernel/proxydock`，可通过 `PROXYDOCK_REPOSITORY=OWNER/REPO` 指定；固定程序版本使用 `PROXYDOCK_VERSION=v1.0.0`。本次分离只创建本地仓库。

## 常用命令

```bash
proxydock --help
proxydock --version

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

`upgrade` 升级 Xray 镜像；程序自身更新通过构建安装或安装脚本完成。`sudo bash scripts/install.sh uninstall` 只卸载程序，保留容器、镜像和数据。

## 文档

- [使用指南](docs/usage.md)：部署、配置、服务、升级回滚及文件位置。
- [迁移说明](docs/migration.md)：旧命令映射和现有实例兼容。
- [开发指南](docs/development.md)：模块边界、测试、构建和发布。

现有部署的目录、Compose 名称、归属标签与状态格式继续沿用 ProxyForge 的历史值，避免迁移数据或重建容器。详见迁移说明。

## 验证

```bash
go test -race ./...
go vet ./...
bash scripts/install_test.sh
make cross
```

普通测试使用模拟 Docker，不安装 Docker、不拉取镜像。真实 Docker、官方镜像及代理连接验收需另行在具备环境的 Linux 主机完成。
