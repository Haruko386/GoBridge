# GoBridge

GoBridge 让固定地址的服务器通过实验室电脑上的 HTTP/SOCKS 代理访问网络。绑定的是机器身份，不是实验室电脑的动态 IP。

```text
服务器 A :17897 -> 加密 Tunnel -> 实验室电脑 B -> 127.0.0.1:7897 -> Internet
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

Docker 容器可以使用 `http://host.docker.internal:17897`；Linux Docker 需要增加：

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

## 管理绑定

```bash
./gobridge status
./gobridge peers
./gobridge disable <peer-name-or-node-id>
./gobridge enable <peer-name-or-node-id>
./gobridge unpair <peer-name-or-node-id>
```

`disable` 和 `unpair` 会使运行中的对应 Tunnel 断开。重新 `enable` 后，在 B 上重新运行 `gobridge connect`。
