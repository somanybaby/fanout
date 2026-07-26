# fanout

把 VPN Gate 的公共节点变成本地 SOCKS5 端口：一个端口一个出口 IP。
可选对接同机的 3x-ui，把面板里的入站按出口分流。

## 原理

每个节点跑在独立的 network namespace 里，netns 内启动官方 openvpn 客户端。
SOCKS5 监听在母机，出站连接用 `setns` 切进对应 netns 建立。

这样做的好处：VPN 的路由劫持只影响自己的 netns，不会切断母机的网络；
多个节点互不干扰，各自一个出口 IP。

```
客户端 ──> 母机 SOCKS5 :随机端口 ──> netns vgN ──> openvpn ──> VPN Gate 节点
```

## 安装

需要 root，Linux（依赖 netns）。

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/byJoey/fanout/main/install.sh)
```

会自动下载对应架构的预编译二进制。也可以 clone 仓库后在源码目录运行同一个脚本，
那样会从源码编译（需要 Go 1.21+）。

装完会打印管理界面地址、访问路径和口令：

```
管理界面  http://<你的IP>:8899/gwPuWHvaNr/
访问口令  f81120ac328d11c11b
```

路径和口令都是随机生成的，分别存在 `/var/lib/fanout/basepath` 和
`/var/lib/fanout/password`。路径不对一律返回 404，扫端口的看不到这里跑着什么。

## 使用

打开管理界面，点「添加节点」选一个节点启动。连通后左栏会显示分配到的
SOCKS5 端口和实际出口 IP，直接用 `socks5://<服务器IP>:<端口>` 即可。

### 对接 3x-ui

同机装了 3x-ui 时，右栏会列出面板里已有的入站。

- **改出口**：在入站行的下拉框里选一个节点，该入站的流量就从那个节点出去。
- **按出口复制**：左栏勾选若干出口，右栏选一个入站作模板，点「按出口复制…」，
  会为每个出口复制一个入站。复制体共用模板的客户端，所以你的 UUID 不变，
  只需要改端口。
- **导出链接**：勾选入站后点「导出链接」，拿到可直接粘贴的分享链接。

面板端口、路径、API token 都是自动探测的，不用手工填。

## 运维

```bash
systemctl status fanout
systemctl restart fanout
journalctl -u fanout -f

cat /var/lib/fanout/basepath   # 访问路径
cat /var/lib/fanout/password   # 访问口令
```

隧道状态存在 `/var/lib/fanout/state.json`，重启后自动恢复，端口保持不变。

健康检查每分钟跑一次，连续两次探测失败就自动换节点重连，槽位和端口不变；
如果对接了 3x-ui，原先指向它的入站会自动改绑到新节点。

## 已知限制

- 只转发 TCP。SOCKS5 收到域名时在本机解析，隧道内不跑 UDP/DNS。
- VPN Gate 是志愿者节点，有相当比例已下线或满员（`AUTH_FAILED`）。
  启动时连不上会自动顺着同地区候选往下试，最多 6 个。
- 管理界面只有随机路径 + 口令登录，没有 HTTPS。放公网建议前面套一层反代。
