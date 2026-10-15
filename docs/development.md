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

Go 模块为 `proxydock`，依赖标准库和 Cobra。Docker 子包的依赖边界由测试检查。

终端颜色输出和版本元数据在本仓库独立维护。`PROXYDOCK_COLOR=always/never` 控制颜色，`NO_COLOR` 和 `TERM=dumb` 支持纯文本输出。`PROXYDOCK_ROOT` 仅用于隔离文件路径；Docker 命令仍连接本机 daemon，不能将它当作容器集成测试的隔离机制。

## 持久化与实例归属

运行目录见[使用指南](usage.md#文件位置)。容器与 Compose 项目为 `proxydock-xray`，标签前缀为 `io.proxydock.*`，`managed_by` 为 `proxydock`。

业务状态保存在 `state.json`，部署元数据保存在 `deployment.json`；协议、schema 和网络字段必须明确记录。证书回收要求归属标记，临时文件按生成格式识别。

所有修改使用实例锁、候选配置校验、备份和事务记录；失败恢复文件、镜像及原运行状态。镜像固定官方 digest，停止实例修改或升级后保持停止。清理核验受管容器及临时容器已移除，限定自己的目录，不运行全局 prune。

## 构建与验证

需要 Go 1.23 或更高版本：

```bash
./scripts/build.sh
./proxydock --version
make check
make cross
```

构建脚本注入 `VERSION`、`COMMIT`、`BUILD_DATE`，可显式提供这些变量；`OUTPUT` 指定输出文件。默认输出 `proxydock`，交叉构建输出 `dist/proxydock-linux-amd64` 和 `dist/proxydock-linux-arm64`。

`make check` 执行 Go race 测试、`go vet`、Shell 语法和安装流程检查。测试使用模拟 Docker、模拟下载和临时目录，不操作系统安装或真实容器。

`scripts/embed.go` 嵌入 `install.sh`，`internal/selfupdate` 负责暂存和执行它。安装脚本处理版本发现、校验、安装与回滚；程序更新和卸载命令由 CLI 层管理。

## 真实运行验收

真实 Docker 和代理连接需在一次性 Linux VM 中另行验收，覆盖 REALITY/TLS 连接、停止状态保持、升级回滚、中断恢复、移除清理及重启恢复。分别记录 amd64 和 arm64 的结果。

## CI 与发布

CI 运行 Go 和安装脚本检查，并构建 Linux amd64/arm64 程序。

仓库为 `JokerKernel/proxydock`。推送 `v` 开头的语义版本标签触发 Release；也可手动选择已有标签。发布前运行 `make check`，随后构建 Linux amd64/arm64 程序，核验版本与标签一致。带预发布后缀的标签发布为 GitHub prerelease，避免进入默认最新正式版选择。发布产物为：

```text
proxydock_linux_amd64_v1.0.0
proxydock_linux_arm64_v1.0.0
version
install.sh
SHA256SUMS
```

`SHA256SUMS` 覆盖两个程序、`version` 和 `install.sh`。安装与程序管理见[安装指南](installation.md)。

[返回项目首页](../README.md)
