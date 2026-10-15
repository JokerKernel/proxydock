# 从 ProxyForge 迁移

本仓库从 ProxyForge 提交 `83713bfdfc22a6e60d7a428bcdfac4a800e966aa` 的工作目录提取 `internal/docker/`，并保留提取时全部 Docker 实现和测试。源码在独立 Go 模块 `proxydock` 内维护，不需要 ProxyForge 源码或二进制。

## 命令

| 旧入口 | 新入口 |
| --- | --- |
| `proxyforge` 主菜单第 3 项 | `proxydock` |
| `proxyforge docker deploy` | `proxydock deploy` |
| `proxyforge docker config …` | `proxydock config …` |
| `proxyforge docker service …` | `proxydock service …` |
| `proxyforge docker cleanup` | `proxydock cleanup` |

上表仅说明命令对应关系。ProxyDock 使用独立的运行目录和实例标识，不接管旧部署。

## 现有部署

当前版本不兼容 ProxyForge 实例，也不自动迁移旧目录、容器、凭据、备份或未完成事务。切换前请自行备份需要保留的配置，并使用旧管理程序处理旧实例及事务；随后使用 ProxyDock 重新部署。如继续使用相同监听端口，请先停止旧实例以释放端口。

| 资源 | 当前值 |
| --- | --- |
| 部署目录 | `/opt/proxydock/xray-docker/` |
| 数据目录 | `/var/lib/proxydock/docker/xray/` |
| 实例锁 | `/run/proxydock/xray-docker.lock` |
| Compose 项目及容器 | `proxydock-xray` |
| 归属标签前缀 | `io.proxydock.*` |
| `managed_by` | `proxydock` |

程序不读取 `/opt/proxyforge/`、`/var/lib/proxyforge/` 或 `/run/proxyforge/`。仅移动旧文件无法转换归属和事务记录。测试根目录使用 `PROXYDOCK_ROOT`。

`proxydock update` 更新 ProxyDock 程序；`proxydock upgrade` 主动升级 Xray 镜像。`proxydock uninstall --yes` 只卸载程序，保留容器、配置、镜像和数据；移除实例与删除数据分别使用 `remove` 和 `cleanup`。

独立仓库为 [JokerKernel/proxydock](https://github.com/JokerKernel/proxydock)。一键安装和程序更新使用该仓库的 Release；源码构建安装仍受支持。详见[安装与更新指南](installation.md)。
