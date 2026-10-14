# 开发、测试与发布

## 目录与依赖

```text
cmd/proxydock/       程序入口
internal/cli/        独立根命令、版本和终端接入
internal/selfupdate/ 程序更新与卸载，执行内置安装脚本
internal/docker/     配置、菜单、Compose、镜像、状态、事务和清理
internal/docker/model/   节点、策略及线路模型
internal/docker/xray/    Xray JSON 渲染和修改
internal/docker/export/  Mihomo YAML 导出
internal/terminal/   自有终端颜色输出
internal/version/    构建元数据
scripts/             构建及程序安装脚本，Go embed 入口
```

Go 模块为 `proxydock`。Docker 业务及其测试仅依赖自身子包、标准库和 Cobra 等通用第三方库；不引用 ProxyForge 的 app/system/provider/domain/install/selfupdate，也没有跨仓库 replace、软链接或运行时调用。依赖边界测试扫描 Docker 子包的生产与测试导入。

终端颜色输出和版本元数据在本仓库独立维护。`PROXYDOCK_COLOR=always/never` 控制颜色，`NO_COLOR` 和 `TERM=dumb` 支持纯文本输出。`PROXYDOCK_ROOT` 仅用于隔离文件路径；Docker 命令仍连接本机 daemon，不能将它当作容器集成测试的隔离机制。

分离前的详细事务及归属设计保留在[历史设计记录](archive/proxyforge-docker-design.md)，涉及旧入口的内容以当前指南为准。

## 持久化兼容

运行目录、锁、Compose 模板、容器名称、标签、`managed_by` 和状态 schema 保持历史格式，保证旧部署及未完成事务可直接恢复。请勿通过批量重命名修改这些持久化值，参见[迁移说明](migration.md)。

所有修改使用实例锁、候选配置校验、备份和事务记录；失败恢复文件、镜像及原运行状态。镜像固定官方 digest，停止实例修改或升级后保持停止。清理核验受管容器及临时容器已移除，限定自己的目录，不运行全局 prune。

## 构建与验证

需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
./proxydock --version
go test -race ./...
go vet ./...
bash -n scripts/build.sh scripts/install.sh scripts/install_test.sh scripts/install_flow_test.sh
bash scripts/install_test.sh
bash scripts/install_flow_test.sh
make cross
```

构建脚本注入 `VERSION`、`COMMIT`、`BUILD_DATE`，可显式提供这些变量；`OUTPUT` 指定输出文件。默认输出 `proxydock`，交叉构建输出 `dist/proxydock-linux-amd64` 和 `dist/proxydock-linux-arm64`。

`make check` 执行以上 Go 和脚本检查。普通测试使用 fake Runner 和临时目录，覆盖配置、生命周期、镜像、归属、回滚、恢复、菜单及独立根命令。安装流程测试模拟下载，在临时安装目录内验证真实文件替换、校验失败、回滚、版本选择、并发锁和卸载保留数据；不会替换系统程序、安装 Docker、拉取镜像或启动容器。

`scripts/embed.go` 直接嵌入同目录下的 `install.sh`，没有第二份脚本副本。`internal/selfupdate` 只负责暂存内置脚本并附接终端执行；版本发现、比较、校验、安装和回滚均由脚本负责。程序更新与卸载由 CLI 入口层管理，不在 Docker 交互菜单中展示，不依赖原生内核业务。

## 真实运行验收

真实 Docker、镜像和端到端代理连接仍待验收。使用一次性 Linux VM，单独确认 REALITY/TLS 部署、客户端实际连接、低端口能力、配置只读挂载、停止状态保持、升级回滚、中断恢复、移除及清理，以及 VM 重启后的恢复。分别记录 amd64 和 arm64 的实际结果。模拟测试通过不能替代这些验收。

## CI 与发布

CI 运行 race 测试、go vet、安装脚本检查及 Linux amd64/arm64 构建。Dependabot 管理 Go Modules 与 Actions 更新。

仓库为 `JokerKernel/proxydock`。推送 `v` 开头的语义版本标签触发 Release；也可手动选择已有标签。发布前运行 `make check`，随后构建 Linux amd64/arm64 程序，核验版本与标签一致。带预发布后缀的标签发布为 GitHub prerelease，避免进入默认最新正式版选择。发布产物为：

```text
proxydock_linux_amd64_v1.0.0
proxydock_linux_arm64_v1.0.0
version
install.sh
SHA256SUMS
```

`SHA256SUMS` 同时覆盖两个程序、`version` 和 `install.sh`。安装脚本识别架构、校验 SHA-256 和运行版本，并原子替换 `/usr/local/sbin/proxydock`；更新程序不会更新容器镜像。卸载程序不会删除实例数据。完整用法见[安装与更新指南](installation.md)。

[返回项目首页](../README.md)
