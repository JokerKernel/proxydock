# ProxyDock 使用指南

ProxyDock 是从 ProxyForge 分离的独立 Docker 代理管理工具，目前管理一个自行创建的 Xray Compose 实例。运行 `sudo proxydock` 进入交互菜单，也可直接使用命令行。

默认输出操作进度，完整 Docker 命令只在添加 `--debug` 时显示，例如 `sudo proxydock --debug`。启动后的稳定性检查每秒查询一次容器，并保留至少 5 秒的观察窗口。

Docker 版不依赖宿主机安装的 Xray，也不要求存在 `xray.service`。使用官方镜像 `ghcr.io/xtls/xray-core`，镜像拉取成功后固定为 digest；普通启动和配置修改不会自动更新镜像。

## 环境要求

- Linux amd64 或 arm64，root 权限。
- 本机 rootful Docker Engine 和 Docker Compose v2 或更高版本插件（含 v5），daemon 已运行。
- 默认使用宿主机网络。也可以选择端口映射：监听端口按相同端口号发布，REALITY 回落仅容器内使用。
- 不支持远程 Docker、rootless/userns-remap、Docker Desktop、自定义桥接网络、多实例和接管任意已有容器。

ProxyDock 只检测 Docker/Compose 是否可用，不会自动安装 Docker，也不会执行 Docker 安装脚本。Docker 版不使用原生 app/system/provider/domain 业务代码。

首次准备、升级需要访问官方镜像仓库。镜像版本支持三种选择：

- `latest` 或省略 `--version`：直接拉取官方 `ghcr.io/xtls/xray-core:latest`；`stable` 是兼容别名。
- `pre-release`：拉取官方 `ghcr.io/xtls/xray-core:pre-release`，选择开发版。
- 具体版本号：拉取对应官方镜像，核对实际内核版本。

三种方式均记录实际内核版本并固定 digest，不查询 GitHub Releases API。标签按官方镜像仓库选择；只有再次主动升级才会拉取标签的新镜像。

## 首次部署

交互模式：运行 `sudo proxydock` 后选择「部署/升级」，填写镜像版本、对外地址、监听端口和 REALITY SNI/target。TLS 模式自动生成自签证书和 SNI。

也可使用命令行：

```bash
# 使用官方 latest 标签；地址和域名需替换为实际值
sudo proxydock deploy \
  --server YOUR_SERVER_IP \
  --port 8443 \
  --sni YOUR_REALITY_SNI \
  --target YOUR_REALITY_TARGET:443

# 指定官方镜像版本（VERSION 替换为实际发行版本号）
sudo proxydock deploy --version VERSION \
  --security tls --server YOUR_SERVER_IP --port 8443

# 使用官方 latest 镜像标签
sudo proxydock deploy --version latest \
  --security tls --server YOUR_SERVER_IP --port 8443
```

默认 `host` 网络与宿主机共享端口，因此原生内核、Docker 实例和其他服务必须使用不同的监听端口。`--network publish` 改为端口映射，监听端口仍使用同一个端口号，REALITY 回落仅容器内使用。已部署实例可以用 `proxydock config set network host` 或 `publish` 切换，切换时会重建容器并保持原来的运行或停止状态。旧版 publish 配置可再次执行 `proxydock config set network publish`，通过事务移除旧的回落端口映射；只读操作不会改写配置。

REALITY 默认启用专用回落防护；`--standard-config` 可选择普通配置，`--fallback-port` 可指定内部回落端口。

```bash
sudo proxydock deploy --network publish \
  --server YOUR_SERVER_IP --port 8443 \
  --sni YOUR_REALITY_SNI --target YOUR_REALITY_TARGET:443

sudo proxydock config set network publish
sudo proxydock config set network host
```

只准备镜像和 Compose：

```bash
sudo proxydock prepare --version VERSION
```

该操作不启动空代理实例。随后通过 Docker 菜单生成配置，或执行 `proxydock config generate`；首次生成有效节点会自动启动。已有停止实例修改配置后保持停止，使用服务入口显式启动。

## 配置与凭据

```bash
sudo proxydock config show
sudo proxydock config validate
sudo proxydock config edit

sudo proxydock config set dns public-cloudflare
sudo proxydock config set log warning
sudo proxydock config set outbound-ip prefer-ipv6
sudo proxydock config set fallback-ip ipv4-only

# 完整生成会覆盖手动配置；默认保留凭据和线路
sudo proxydock config generate --port 8443 --force

# 重置 UUID；REALITY 同时轮换密钥和 short ID
sudo proxydock config reset --yes

# TLS 仅轮换证书；若需同时轮换 UUID，再加 --credentials
sudo proxydock config reset --rotate-certificate --yes
```

DNS 支持 `system`、`public-cloudflare`、`public-google`、`doh-cloudflare`、`doh-google`；地址族策略支持 `prefer-ipv4`、`prefer-ipv6`、`ipv4-only`、`ipv6-only`、`unset`。

编辑器打开候选副本，校验通过后才提交正式文件。主节点和落地线路的受管 UUID、密钥、监听地址、端口、传输方式、SNI 与证书引用，以及 REALITY 回落入站和目标，通过专用入口修改；手动编辑破坏这些字段时会拒绝提交。其他无关 JSON 字段可保留。

切换已有主节点协议且有线路时，需要先移除线路或显式使用 `--drop-links`。`--rotate-credentials` 与 `--rotate-certificate` 可用于生成过程中主动轮换。

## 访问控制与线路

```bash
sudo proxydock config access add white example.com 10.0.0.0/8
sudo proxydock config access add black blocked.example.com --exact
sudo proxydock config access mode whitelist
sudo proxydock config access switch off
sudo proxydock config access list
sudo proxydock config access clear white

# 在落地服务器创建接入并导出不含私钥的 peer 文件
sudo proxydock config landing add upstream \
  --security tls --port 8444 --output /root/upstream-peer.json

# 安全复制 peer 文件到中转服务器，文件权限保持 0600
sudo proxydock config relay add relay-one \
  --peer-file /root/upstream-peer.json

sudo proxydock config landing disable upstream
sudo proxydock config relay enable relay-one
sudo proxydock config relay remove relay-one
```

白名单优先于黑名单和默认私网阻断；`whitelist` 模式只允许白名单，空白名单拒绝代理请求。访问策略关闭时保留名单数据，之后可通过 `switch on` 恢复。规则作用于代理请求目标，REALITY 回落继续使用专用限制。

落地 `reality` 接入共享 REALITY 主入站；独立 TLS 接入使用自己的端口和证书。TLS 主节点新增落地接入时选择 `--security tls`。

交互菜单中的“使用当前协议”或 CLI 的 `landing add --security current` 共享主节点端口；TLS 主节点也共享其证书。CLI 的 `landing add --security tls` 继续创建独立 TLS 入站，省略端口时自动选择随机高位端口。

## 客户端导出

```bash
sudo proxydock config client --output /root/xray-client.json
sudo proxydock config client --format clash --output /root/mihomo-client.yaml
sudo proxydock config client --name relay-one --format clash
sudo proxydock config landing peer upstream --output /root/upstream-peer.json
```

导出支持主节点、启用的落地接入和中转线路；文件默认以 `0600` 权限创建，覆盖已有导出文件需要 `--force`。服务端配置、客户端和 peer 信息包含凭据，应妥善保存。

客户端配置从本地受管配置生成，不调用 Docker；原生 JSON 为 Xray 格式。存在未完成事务时，客户端导出、落地 peer 导出、名单/线路列表和服务端配置校验会提示先执行 `proxydock recover`，不会自动恢复或重建业务容器。

## 服务、升级与恢复

```bash
sudo proxydock status
sudo proxydock service start
sudo proxydock service stop
sudo proxydock service restart
sudo proxydock service logs
sudo proxydock service logs --follow

sudo proxydock upgrade --version VERSION
sudo proxydock upgrade --version latest
sudo proxydock recover
```

升级先拉取镜像并校验当前配置，再重建容器，保留节点凭据、证书、名单和线路。运行中的实例重建后核验镜像、监听端口和持续运行情况；已停止实例升级后保持停止。

配置或升级失败时恢复旧文件和原运行状态。回滚无法完成会保留恢复记录并显示错误；下次写操作会尝试恢复，也可显式执行 `recover`。不要手动删除 `transaction.json` 或它引用的备份。

名单的增加、删除、模式和开关变更会在恢复事务后，基于最新已提交状态执行，防止恢复结果被旧名单覆盖。删除 TLS 落地线路后可以同名重建；重建使用新证书目录，旧证书保留供历史恢复记录使用。

事务提交、成功回滚和恢复后保留最近 3 份程序生成的备份。证书回收会同时检查当前状态、服务端配置和保留备份的引用；无法读取备份时暂停证书回收并显示清理信息。`recover` 也会回收异常退出遗留的受管临时文件和校验容器，不会启动已停止的实例。

镜像更新仍会拉取选定标签确认 digest；镜像未变化时跳过能力探测和容器重建。重复设置相同策略、启动已运行实例或停止已停止实例时，会跳过不必要的校验和事务。日志直接从已核验归属的容器读取，不依赖 Compose 文件是否完整。

历史状态旁路文件继续保留，内容未变化时不重复写入。

## 移除与清理

```bash
sudo proxydock remove --yes
sudo proxydock cleanup --yes
```

`remove` 只移除容器，保留配置、凭据、证书、备份和镜像；重新部署可沿用配置。`cleanup` 只有在确认本实例和临时容器均已移除后，才永久删除自有数据。

`recover`、`remove` 和 `cleanup` 会处理归属核验通过的遗留校验容器；`remove` 和 `cleanup` 还会删除本 Compose 项目未被任何容器使用的默认网络。其他容器、网络和镜像不参与回收。

完整清理开始前会写入 `/var/lib/proxyforge/docker/xray-cleanup.json`。删除中途失败或进程中断时，记录保留，状态显示“清理未完成”；修复故障后再次执行 `proxydock cleanup --yes` 即可继续，完成后记录自动删除。清理尚未完成时拒绝部署及配置写入，请勿手动删除这份记录。

ProxyDock 只清理自己的 Docker 实例，不协调原生内核清理。ProxyForge 的 `cleanup all` 保留历史 Docker 数据，由 `proxydock cleanup` 单独清理。Docker daemon 不可达且无法完成归属检查时，ProxyDock 拒绝清理。

不自动删除 Docker Engine、共享镜像或其他容器，不运行全局 prune。

## 文件位置

为接管已有实例，以下路径、Compose 项目/容器名称 `proxyforge-xray`、`io.proxyforge.*` 标签及 `managed_by=proxyforge` 归属字段继续沿用历史值。新部署也使用同一布局；不需要迁移数据，不要同时用旧版 ProxyForge 管理同一实例。程序名、Go 模块、安装产物及发布流程使用 `proxydock`。

| 内容 | 路径 |
| --- | --- |
| Compose | `/opt/proxyforge/xray-docker/compose.yaml` |
| 服务配置 | `/opt/proxyforge/xray-docker/config/config.json` |
| 证书 | `/opt/proxyforge/xray-docker/tls/` |
| 自有状态和部署记录 | `/var/lib/proxyforge/docker/xray/` |
| 备份 | `/var/lib/proxyforge/docker/xray/backups/` |
| 事务记录 | `/var/lib/proxyforge/docker/xray/transaction.json` |
| 实例锁 | `/run/proxyforge/xray-docker.lock` |

目录归属由 `owner.json` 标记，部署和节点 JSON 也有独立的实例标识与 schema。管理目录为 `0700`，配置、证书和管理文件为 `0600`。容器以 UID/GID `0:0` 读取只读挂载，仅保留绑定低端口所需的 capability。

Compose 由程序生成并校验摘要；外部直接修改 Compose 后，程序会拒绝按未知定义操作。通过菜单和命令选择版本及修改代理配置。

## 当前验收范围

已提供模拟 Docker 的单元测试、独立命令入口和依赖边界检查。真实 Docker、官方镜像、端到端代理连接及 VM 重启验收需在已安装 Docker 的 Linux 环境完成；开发过程中未自动安装 Docker。

[开发指南](development.md) · [返回项目首页](../README.md)
