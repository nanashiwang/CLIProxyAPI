# CPA 节点标准化部署

用于批量部署 `cpa024`、`cpa025`……`cpa0100`，配置以现有 `cpa024` 为基础，但避免手工执行大量 `sed`、systemd、ACME 和 Nginx 命令。

## 主要优化

- 固定从 `nanashiwang/CLIProxyAPI` 的个人 Release 安装。
- 节点号、域名和密钥统一通过环境文件传入。
- 自动读取本机 sing-box SOCKS5 用户、密码和端口。
- CPA 只监听 `127.0.0.1:8317`。
- 自动配置 systemd、ECC 证书和 Nginx。
- Nginx 支持 WebSocket、长连接、100 MB 请求、管理接口限速及可选 IP 白名单。
- 默认开启商业模式。
- CPA 日志总量默认限制为 512 MB，避免磁盘被无限占满。
- 密钥不写入 Git 仓库。
- 安装后使用统一命令升级，升级命令每次都会刷新自身和个人 Release 地址。
- 管理面板固定从个人仓库 Release 更新，避免误用上游旧版本。
- 默认开启持久化使用统计。

## 前置条件

1. 域名已经解析到服务器公网 IP。
2. sing-box 已安装并启动。
3. `/etc/sing-box/config.json` 中至少有一个带用户密码的 inbound。
4. TCP 22、80、443 已在云防火墙中放行。
5. ACME ALPN 申请证书时需要临时停止 Nginx，占用约数秒。

## 部署

将目录复制到服务器后执行：

```bash
cd /root/cpa-node
cp cpa-node.env.example /root/cpa-node.env
chmod 600 /root/cpa-node.env
nano /root/cpa-node.env
bash install.sh /root/cpa-node.env
```

新节点通常只需要修改：

```bash
CPA_NODE="cpa025"
CPA_DOMAIN="cpa025.meta-api.vip"
CPA_MANAGEMENT_KEY="新的高强度管理密钥"
CPA_API_KEYS="API密钥1,API密钥2"
CPA_COMMERCIAL_MODE="true"
CPA_ADMIN_CIDR="管理员公网IP/32"
```

密钥可以这样生成：

```bash
openssl rand -hex 32
```

如果管理员公网 IP 不固定，可以暂时将 `CPA_ADMIN_CIDR` 留空；留空时管理入口仍需要 Management Key，但会对公网开放。

不要把真实的 `/root/cpa-node.env` 提交到 Git。

## 更新 CPA 和管理面板

```bash
update-cliproxyapi
```

更新命令会执行以下操作：

1. 刷新个人 Release 安装器，避免服务器保留旧版安装器。
2. 确保管理面板仓库为 `nanashiwang/Cli-Proxy-API-Management-Center`。
3. 确保持久化使用统计已开启。
4. 执行后端升级、`systemctl daemon-reload` 和服务重启。
5. 服务启动后由 CPA 自动检查并更新 `management.html`。

如果是旧节点，第一次先执行一次迁移命令；执行成功后，以后仍然直接使用 `update-cliproxyapi`：

```bash
curl --retry 5 --retry-all-errors -fsSL \
  https://raw.githubusercontent.com/nanashiwang/CLIProxyAPI/main/deploy/cpa-node/update-node.sh | bash
```

## 批量更新多个节点

建议使用 SSH 密钥，不要在脚本中保存服务器密码。创建一个本地清单，例如 `servers.tsv`：

```text
# 节点	地址	端口
cpa025	38.244.50.196	22
cpa026	203.0.113.26	22
cpa027	203.0.113.27	22
```

执行：

```bash
bash deploy/cpa-node/update-many.sh servers.tsv
```

脚本会逐台执行更新，单台失败不会中断剩余节点，最后汇总成功和失败数量。

## 检查

```bash
systemctl status sing-box cliproxyapi nginx --no-pager
nginx -t
curl -I https://当前节点域名/management.html
curl https://当前节点域名/healthz
ss -lntp | grep -E ':8317|:443|:80'
```

正常情况下，8317 只应监听在 `127.0.0.1`，公网只开放 80、443 和 SSH 端口。
