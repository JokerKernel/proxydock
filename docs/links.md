# 中转与落地线路

每条中转线路使用独立客户端 UUID，固定连接指定落地。客户端连接中转服务器的主入站；中转服务器再使用落地连接文本中的 UUID 连接上游。普通用户继续使用本机出口，中转连接失败时不会自动回退本机出口。访问控制仍按最终请求目标匹配，作用于受管中转用户和落地入口。

## 创建落地接入

在落地服务器执行：

```bash
# 默认复用当前 REALITY/TLS 主入站及端口，创建独立接入用户
sudo proxydock config landing add from-relay

# 独立 TLS 入站，自动分配高位端口并生成自签证书
sudo proxydock config landing add from-relay-tls --security tls

# 指定独立 TLS 端口并导出连接文本
sudo proxydock config landing add office --security tls --port 8444 \
  --output /root/office-landing.json

# 再次获取完整连接文本；show 是同一操作
sudo proxydock config landing export from-relay
sudo proxydock config landing show from-relay
sudo proxydock config landing list
```

独立 TLS 入站需放行对应 TCP 端口的防火墙和云安全组；中转使用证书固定指纹验证。`--security reality` 仅用于 REALITY 主节点。`landing peer` 保留原有裸 peer JSON 输出；`add/export/show` 输出带标识和版本的完整连接文本。输出文件使用 `0600` 权限；覆盖已有文件使用 `--force`。

## 导入和管理中转

在中转服务器的“中转与落地线路 → 添加中转线路”中选择：

1. 打开临时编辑文件，粘贴完整连接 JSON，保存退出。
2. 手动输入落地内核、REALITY/TLS 协议、地址、端口、SNI、UUID 和相应公钥或指纹。
3. 读取已有的 `0600` 落地连接 JSON 文件。

编辑器依次查找 vim、nano、vi。临时目录为私有目录，完成、取消或失败时清理文件及编辑器备份、交换文件；活动编辑持有目录锁，后续维护会回收已中断的非活动会话。

命令行支持文件和标准输入：

```bash
# 文件须通过可信方式传到中转机，保持 0600 权限
sudo proxydock config relay add office --peer-file /root/office-landing.json

# 也可粘贴完整 JSON，结束后按 Ctrl+D
sudo proxydock config relay add other --upstream-stdin

sudo proxydock config relay list
sudo proxydock config relay test office
sudo proxydock config relay client office --format native
sudo proxydock config relay client office --format clash --output /root/office-client.yaml

# 替换上游，保留本机中转客户端 UUID、用户名和启停状态
sudo proxydock config relay update office --upstream /root/new-landing.json

sudo proxydock config relay disable office
sudo proxydock config relay enable office
sudo proxydock config relay remove office
```

支持当前 ProxyDock 连接文本、已有裸 peer JSON，以及 ProxyForge 版本 1–3 的落地连接文本；上游可为使用同一 VLESS + RAW + Vision 协议的 Xray 或 sing-box。本机继续只管理 Xray Docker。TLS 连接文本必须包含合法证书指纹；REALITY 必须包含合法公钥和 short ID。不接受包含服务端私钥或未知字段的文本，输入上限为 64 KiB。

添加或更新前进行 TCP 连通性检查，默认超时 3 秒。明确允许暂时不可达时，可加 `--allow-unreachable`；菜单也会提供继续保存的选择。这个选项不绕过配置校验、地址校验或操作取消。`relay test` 只检查 TCP 连接，不验证 UUID、TLS/REALITY 认证或完整代理链路。

落地 IP 允许公网单播地址和 `192.168.0.0/16` 联调地址；拒绝其他私网、回环和保留 IP，以及直接指向本机配置地址和受管入口端口的循环。该例外不改变用户代理请求原有的私网拦截规则。

## 轮换和停用

```bash
sudo proxydock config landing rotate from-relay --yes
sudo proxydock config landing export from-relay

sudo proxydock config relay rotate office --yes
sudo proxydock config relay client office --format clash

sudo proxydock config landing disable from-relay
sudo proxydock config landing enable from-relay
sudo proxydock config landing remove from-relay
```

落地 UUID 轮换后，需要把新连接文本更新到所有使用它的中转机；中转 UUID 轮换后，只需更新该线路的客户端。轮换保留其他用户、上游信息、证书、端口和启停状态。停用线路会移除相应入站用户，启用后恢复；停用时不能导出可用客户端或落地连接文本。

线路名称为 1–48 个字母、数字、下划线或连字符，首位必须为字母或数字；保留名称及与已有用户、标签、接入或线路冲突的名称会被拒绝。

所有线路配置修改沿用实例锁、Docker Xray 候选校验、备份和事务回滚。UUID 和连接文本包含凭据，导出及测试等只读操作遇到未完成事务时会要求先恢复。

[返回使用指南](usage.md)
