# ProxyDock 使用指南

ProxyDock 管理一个自行创建的 Xray Docker Compose 实例。运行 `sudo proxydock` 进入交互菜单，也可直接使用命令行。

默认输出操作进度，添加 `--debug` 可查看完整命令，例如 `sudo proxydock --debug`。

Docker 版不依赖宿主机安装的 Xray，也不要求存在 `xray.service`。使用官方镜像 `ghcr.io/xtls/xray-core`，镜像拉取成功后固定为 digest；普通启动和配置修改不会自动更新镜像。

## 环境要求

- Linux amd64 或 arm64，root 权限。
- 本机 rootful Docker Engine 和 Docker Compose v2 或更高版本插件，daemon 已运行。
- 默认使用端口映射：监听端口按相同端口号发布，REALITY 回落仅容器内使用。也可以选择宿主机网络。
- 不支持远程 Docker、rootless/userns-remap、Docker Desktop、自定义桥接网络、多实例和接管任意已有容器。

Docker 和 Compose 需要提前安装。

首次准备、升级需要访问官方镜像仓库。镜像版本支持三种选择：

- `latest`：直接拉取官方 `ghcr.io/xtls/xray-core:latest`，首次准备或部署省略 `--version` 时默认使用。
- `pre-release`：拉取官方 `ghcr.io/xtls/xray-core:pre-release`，选择开发版。
- 具体版本号：拉取对应官方镜像，核对实际内核版本。

三种方式均记录实际内核版本并固定 digest，只有主动升级才会拉取标签的新镜像。

更新菜单默认沿用已保存的渠道或固定版本；`upgrade` 省略 `--version` 时行为相同。选择渠道或指定版本后，需确认执行安装或更新才会保存，返回菜单不会保存尚未应用的选择。

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

# TLS 节点，自动生成证书；可加 --version 指定镜像版本
sudo proxydock deploy \
  --security tls --server YOUR_SERVER_IP --port 8443
```

新部署默认使用 `publish` 端口映射，监听端口按相同端口号发布，REALITY 回落仅容器内使用。`--network host` 使用宿主机网络栈，监听端口须与其他服务错开。已有实例省略 `--network` 时沿用当前方式；切换网络会重建容器，并保持原来的运行或停止状态。

REALITY 默认启用专用回落防护；`--standard-config` 可选择普通配置，`--fallback-port` 可指定内部回落端口。

```bash
sudo proxydock deploy --network publish \
  --server YOUR_SERVER_IP --port 8443 \
  --sni YOUR_REALITY_SNI --target YOUR_REALITY_TARGET:443

sudo proxydock config set network publish
sudo proxydock config set network host
```

只准备镜像和部署参数：

```bash
sudo proxydock prepare --version VERSION
```

该操作校验镜像并保存版本、网络方式和运行用户等部署参数，不写入 Compose 或正式节点配置，不启动代理实例。随后通过 Docker 菜单生成配置，或执行 `proxydock config generate`；首次生成有效节点时写入配置和 Compose，并自动启动。生成配置前更新镜像、切换网络或运行用户也只更新部署参数。生成、编辑或修改节点配置后会自动应用并确保容器运行；已停止时启动，已移除时重新创建。配置校验失败不应用，启动或应用失败则恢复修改前的配置与容器状态。

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

访问模式支持自动选择和两种手动选择：自动选择时，白名单有内容就使用 `whitelist`，仅允许白名单中的地址和端口；清空白名单或删除最后一条后自动恢复默认模式。单独添加黑名单不切换模式。手动选择 `default` 后始终使用默认模式；手动选择 `whitelist` 后始终仅允许白名单，清空或删除最后一条后仍拒绝全部代理请求。即使界面当前已显示默认模式，明确选择默认模式也会记录手动选择。关闭访问控制期间增删规则只更新保存的策略，不自动打开开关。

菜单“切换访问模式 → 恢复自动选择”或 `config access mode auto` 可恢复持续的自动判断，后续增删白名单都会重新决定生效模式，同时保留两份名单和开关状态。自动选择时 `mode` 字段为空或省略，手动选择分别保存为 `default` 或 `whitelist`；界面显示当前生效模式及“自动选择”标记。已有配置中明确保存的模式继续保持固定；需要跟随名单变化时，可主动恢复自动选择。

菜单“添加规则”和命令行均支持批量添加，用空格、英文逗号或中文逗号分隔域名、IP 和 CIDR，例如 `example.com example.org，1.2.3.4,10.0.0.0/8`。域名统一选择是否包含子域名；重复规则自动去重，任一目标无效时整批不提交。

每条规则可单独指定目标端口。新添加的域名默认匹配 `80,443`，IP/CIDR 默认匹配所有端口；旧规则保持原有范围。支持单端口、多个端口及范围，例如 `80,443,8000-8100`，有效端口为 1–65535，`all` 表示所有端口。菜单在选择域名匹配方式后逐条询问端口，回车使用该地址类型的默认值；任一端口无效或取消时整批不提交。命令行 `--ports` 应用于本次命令的每个目标；不同端口条件分次添加。同一地址可保存多条不同端口的规则；删除时需提供对应的 `--ports` 和域名匹配方式，省略端口时域名按 `80,443` 匹配、IP/CIDR 按所有端口匹配。删除旧的不限端口域名规则需指定 `--ports all`，也可在菜单中按编号删除。

端口字段也支持点号分隔：`80.443.8443` 等同于 `80,443,8443`，可混合范围，如 `80.443.8000-8100`。英文逗号、中文逗号和点号可以混用，保存及生成配置时统一使用英文逗号；域名/IP 目标字段的解析方式保持不变。

规则同时匹配地址和目标端口才生效：黑名单只拦截命中的端口；仅白名单模式只放行命中的地址与端口，其他连接拒绝；默认模式下未命中白名单的连接继续按后续规则处理。白名单仍优先于黑名单。生成 Xray 配置时，先合并同一地址、同一匹配方式的端口，再合并地址类型和端口条件相同的目标。例如同一域名的 `80,443` 和 `553` 会生成一条 `80,443,553` 规则；完整域名与包含子域名的规则分别处理，其他域名的端口范围保持原样。合并仅在各自名单和出站范围内进行，管理列表仍逐条保存，可独立删除原规则；原生客户端的白名单例外路由同步使用合并结果。

若同一域名已有匹配所有端口的白名单规则，新增限定端口的规则不会缩小原有放行范围；要收窄范围，需先删除原来的不限端口规则。

```bash
sudo proxydock config access add white example.com 10.0.0.0/8
sudo proxydock config access add black blocked.example.com --exact
sudo proxydock config access add white a.example.com --ports 443
sudo proxydock config access add white b.example.com --ports 80,443
sudo proxydock config access add white all-ports.example.com --ports all
sudo proxydock config access add black 10.0.0.0/8 --ports 8000-8100
sudo proxydock config access remove white a.example.com --ports 443
sudo proxydock config access mode default
sudo proxydock config access mode whitelist
sudo proxydock config access mode auto
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

白名单优先于黑名单和默认私网阻断；`whitelist` 模式只允许白名单，空白名单拒绝代理请求，黑名单保留但不参与匹配。清空名单仅删除名单内容，保留访问模式和开关状态；在启用访问控制的 `whitelist` 模式下清空白名单后，全部代理请求仍被拒绝，恢复放行需添加白名单或显式切换模式。访问策略关闭时保留名单数据，之后可通过 `switch on` 恢复。规则作用于代理请求目标，REALITY 回落继续使用专用限制。

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

sudo proxydock upgrade # 沿用当前渠道或固定版本
sudo proxydock upgrade --version VERSION
sudo proxydock upgrade --version latest
sudo proxydock recover
```

升级先拉取镜像并校验当前配置，再重建容器，保留节点凭据、证书、名单和线路。运行中的实例重建后核验镜像、监听端口和持续运行情况；已停止实例升级后保持停止。更新成功并提交部署记录后，删除本次替换的旧镜像；更新失败时保留旧镜像用于回滚。镜像未变化时不删除；Docker 拒绝删除旧镜像时显示原因，已完成的更新仍然有效。

首次部署、配置应用、镜像升级及运行用户或网络切换使用事务保护，失败时恢复旧文件和原运行状态。普通服务启停、重启以及仅切换更新渠道不创建回滚事务。回滚无法完成会保留恢复记录并显示错误；下次写操作会尝试恢复，也可显式执行 `recover`。不要手动删除 `transaction.json` 或它引用的临时事务记录。

`backups/<时间戳>/config.json` 直接保存原始配置文件，不编码，不备份 Compose、管理数据、TLS 证书或私钥。首次部署成功后保存第一份实际配置，后续配置内容变更前备份旧配置，保留最近 3 份。升级镜像、切换运行用户或网络不会创建配置备份。

`transactions/<时间戳>/config.json` 单独保存回滚用的原始配置，调用方便，与正式备份独立；`snapshot.json` 保存 Compose、管理状态、部署记录，以及配置原始权限和校验值。恢复前核验事务自己的配置副本，不依赖 `backups/` 中的文件，也不阻止配置备份轮换。事务结束后清理临时记录，恢复未完成时保留。`recover` 还会回收异常退出遗留的临时文件和校验容器，并清理无引用的受管证书。

## 移除与清理

```bash
sudo proxydock remove --yes
sudo proxydock cleanup --yes
```

`remove` 只移除容器，保留配置、凭据、证书、备份和镜像；重新部署可沿用配置。`cleanup` 先停止并移除本实例容器，确认移除后再永久删除自有数据。停止或移除失败则中止删除。

如存在不属于 ProxyDock 的同名 `proxydock-xray` 容器，清理前会显示其容器 ID、镜像 ID 和状态，明确确认后才停止并移除该容器；取消则保留容器与实例数据。命令行 `cleanup --yes` 表示确认该清理，包括同名容器；安装、更新等其他入口仍拒绝操作外部同名容器。

清理会删除下表中的部署目录、实例数据，以及本项目的空闲默认网络。保留管理程序、Docker、镜像、用户导出文件、锁文件及其他实例的资源。清理后可重新安装并生成配置。完整卸载程序见[安装指南](installation.md#卸载管理程序)。

清理中断后会保留 `xray-cleanup.json`，再次执行 `proxydock cleanup --yes` 可继续。请勿手动删除该记录；Docker 不可达或归属核验失败时会拒绝清理。

## 文件位置

Compose 项目和容器名称均为 `proxydock-xray`。部署目录为 `/opt/proxydock/xray-docker/`，实例数据目录为 `/var/lib/proxydock/docker/xray/`。

| 内容 | 路径 |
| --- | --- |
| Compose | `/opt/proxydock/xray-docker/compose.yaml` |
| 服务配置 | `/opt/proxydock/xray-docker/config/config.json` |
| 证书 | `/opt/proxydock/xray-docker/tls/` |
| 自有状态和部署记录 | `/var/lib/proxydock/docker/xray/` |
| 节点、凭据、名单和线路 | `/var/lib/proxydock/docker/xray/state.json` |
| 镜像、网络和部署状态 | `/var/lib/proxydock/docker/xray/deployment.json` |
| 备份 | `/var/lib/proxydock/docker/xray/backups/` |
| 事务记录 | `/var/lib/proxydock/docker/xray/transaction.json` |
| 临时回滚数据 | `/var/lib/proxydock/docker/xray/transactions/` |
| 临时配置 | `/var/lib/proxydock/docker/xray/staging/` |
| 清理恢复记录 | `/var/lib/proxydock/docker/xray-cleanup.json` |
| 实例锁 | `/run/proxydock/xray-docker.lock` |

管理目录权限为 `0700`，配置、证书和管理文件为 `0600`。

Compose 由程序生成并校验摘要；外部直接修改 Compose 后，程序会拒绝按未知定义操作。通过菜单和命令选择版本及修改代理配置。

[开发指南](development.md) · [返回项目首页](../README.md)
