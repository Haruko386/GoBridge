# GoBridge

GoBridge 让固定地址的服务器通过实验室电脑上的 HTTP/SOCKS 代理访问网络。绑定的是机器身份，不是实验室电脑的动态 IP。

```text
服务器 A 127.0.0.1:17897 -> 加密 Tunnel -> 实验室电脑 B -> 127.0.0.1:7897 -> Internet
```

## 构建

两台 Linux 主机分别执行：

```bash
go build -o gobridge ./cmd/gobridge
```

## 首次配置

服务器 A 有固定 IP，运行：

```bash
./gobridge init --role server
./gobridge pair create
```

记下输出的 Pair Code。保持 `pair create` 运行，然后在实验室电脑 B 上执行：

```bash
./gobridge init --role client
./gobridge pair A_FIXED_IP:18790 --code XXXX-XXXX
```

B 默认把流量转发到 `127.0.0.1:7897`。如果 Clash/Mihomo 使用其他端口，修改 B 的 `~/.gobridge/config.yaml`：

```yaml
client:
  proxy_address: 127.0.0.1:7897
```

## 启动

在服务器 A 上：

```bash
./gobridge serve
```

在实验室电脑 B 上：

```bash
./gobridge connect
```

然后在 A 上测试：

```bash
export HTTP_PROXY=http://127.0.0.1:17897
export HTTPS_PROXY=http://127.0.0.1:17897
curl -I https://example.com
```

A 始终使用自己的 `127.0.0.1:17897`，不需要保存或修改 B 的 IP。B 断网、重启或 IP 改变后，`gobridge connect` 会自动重连 A。

代理入口默认只监听 A 的回环地址，不接受其他主机的连接。旧配置如果包含 `proxy_listen: :17897`，请将它改成 `proxy_listen: 127.0.0.1:17897`。

如果 Docker 容器需要使用该代理，将 A 的 `proxy_listen` 显式改成 Docker 网桥的网关地址，例如：

```yaml
server:
  control_listen: :18790
  proxy_listen: 172.17.0.1:17897
```

然后容器使用 `http://host.docker.internal:17897`。Linux Docker Compose 还需要增加：

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

Docker 网桥地址可能不同，请以本机实际配置为准，并使用防火墙确保 `17897` 只允许可信容器网段访问。不要在不受信任的网络上配置 `proxy_listen: :17897` 或 `0.0.0.0:17897`。

## 管理绑定

```bash
./gobridge status
./gobridge peers
./gobridge disable <peer-name-or-node-id>
./gobridge enable <peer-name-or-node-id>
./gobridge unpair <peer-name-or-node-id>
```

`disable` 和 `unpair` 会使运行中的对应 Tunnel 断开。重新 `enable` 后，在 B 上重新运行 `gobridge connect`。
