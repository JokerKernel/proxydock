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

ProxyForge 不再提供 Docker 入口；其菜单第 3 项恢复为原生卸载残留清理。原生 `cleanup all` 保留 `/var/lib/proxyforge/docker/`，不依赖 Docker daemon，也不会删除 ProxyDock 的记录。

## 现有部署

容器名称、Compose 项目、目录、锁、归属标签、状态 schema 和事务格式保持兼容。安装 ProxyDock 后无需重建容器或移动数据，先运行 `sudo proxydock status`；如有未完成事务，运行 `sudo proxydock recover`。

历史路径和 `managed_by=proxyforge` 是持久化兼容标识，不是源码依赖。现有 Compose 文件的摘要仍有效。不要同时通过旧版 ProxyForge 与 ProxyDock 修改同一实例。测试根目录使用 `PROXYDOCK_ROOT`，替代原入口的 `PROXYFORGE_ROOT`。

`proxydock update` 更新 ProxyDock 程序；`proxydock upgrade` 主动升级 Xray 镜像。`proxydock uninstall --yes` 只卸载程序，保留容器、配置、镜像和数据；移除实例与删除数据分别使用 `remove` 和 `cleanup`。

独立仓库为 [JokerKernel/proxydock](https://github.com/JokerKernel/proxydock)。一键安装和程序更新使用该仓库的 Release；源码构建安装仍受支持。详见[安装与更新指南](installation.md)。
