# 稳定性与压力测试规范

本规范把可重复的代码验证、隔离 Linux 交付和真实路由器验收分开记录。探针与分析器没有默认目标；真实网络操作需要 `-run-live` / `--run-live`。**CI 通过不等于路由器无线、WAN 线速、游戏或长期运行验收通过。**

## 测试层次

| 层次 | 环境 | 检查内容 | CI |
| --- | --- | --- | --- |
| 离线逻辑 | 临时目录、合成记录与 PCAP | 记录完整性、结果判定、包身份配对、心跳原子更新、过期回退、owner 隔离 | Python 3.11 / 3.13 |
| 本机网络 | 临时 loopback HTTP/UDP server | 连接复用、计划结束、无响应、短响应、HTTP 500、并发字节预算、UDP 丢失与损坏、CLI 退出状态 | Go race + Python CLI 集成 |
| Linux UDP 交付 | GitHub runner 内部双栈 Docker 网络 | 固定 Mihomo 补丁、真实 EADDRINUSE、RAW 回包、并发与 FD 生命周期、独立 IPv4/IPv6 peer | 独立 job |
| RN01 实机 | 明确授权的设备与网络 | TPROXY 规则命中、切换连续性、分流性能、负载、Wi-Fi/驱动行为、故障恢复 | 手动，不由 CI 连接家庭设备 |
| 真实应用 | 电视、游戏电脑及相应服务 | 咪咕投屏、CS2/完美平台及实际长时间体验 | 人工验收 |

不上传真实订阅、配置、配对文件、私钥或家庭网络抓包。测试仅使用拥有或获得授权的设备与目标；公网目标需要考虑服务条款、带宽配额与限速，不能把公共站点当作无限压力服务器。

## 本地运行 CI 对应检查

从仓库根目录执行，需要 Python 3.11+ 和 Go 1.27：

```sh
python3 -m pip install -r requirements.txt
python3 -m unittest discover -s tests -p 'test_*.py' -v
python3 -m unittest discover -s tests/stability -p 'test_*.py' -v
go -C tests/netprobe test -race -count=1 ./...
mkdir -p build
go -C tests/netprobe build -o "$PWD/build/netprobe" .
NETPROBE_BINARY="$PWD/build/netprobe" \
  python3 -m unittest discover -s tests/stability -p 'test_cli.py' -v
```

未设置 `NETPROBE_BINARY` 时，仅 CLI 网络集成测试会明确跳过；GitHub Go job 构建二进制后执行它们，不允许将缺少二进制算作网络测试通过。其余离线回归不需要接口权限、Docker 或网络设备。

Linux 交付 job 使用 Go 1.26.8 编译固定补丁中的测试，再调用 `scripts/test-udp-isolated-linux.sh`。运行时使用内部网络、scratch 镜像、只读测试文件及仅发送端所需的 NET_RAW 能力；不下载运行时镜像、不修改宿主路由或家庭路由器。脚本只清理本次创建的容器/网络。发送端与独立 peer 的退出码都必须为零；能力缺失或环境不支持时应失败，不静默跳过。

容器 fixture 会先从已分配的 IPv6 地址向 peer 的 discard 端口发送一个字节，建立普通 NDP 邻居解析，再验证非本地源地址回包。否则首次伪源发送可能只返回写入成功、却没有可用的目的邻居；发送端成功不能代替实际 peer 收到数据。此准备逻辑仅加入 CI 测试入口，不改动发布内核源码补丁；两端交付检查仍完整执行。

## 通用探针

`tests/netprobe` 支持 macOS/Linux，不依赖 RN01。输出为 JSONL，包含 `run-start`、测量记录及 `run-end`；进程崩溃或强制终止时，缺少结束记录意味着证据不完整。探针不修改代理开关、节点、路由或防火墙。

HTTP 每次运行有全局读取字节预算，所有 worker 共享，最大 512 MiB；持续时间不超过 30 分钟，最多 8 个 worker、每 worker 最多 1000 次请求。TLS 正常验证，不提供跳过证书校验开关，不跟随重定向。HTTP 除 loopback 外需显式 `-allow-http`，只用于受控 peer。`-read-rate` 限制应用读取速度，不是严格的网卡整形；TCP/HTTP 可能预取数据，读取预算也不等于严格的 WAN 字节上限。严格流量限制需要受控服务端限制响应长度并独立统计链路字节。

以下变量由操作者提供，URL 不应含账号或令牌；不要复制私人路径或假定固定 SSID/MAC：

```sh
RUN_DIR="logs/stability-$(date +%Y%m%d-%H%M%S)"
mkdir -m 700 "$RUN_DIR"
build/netprobe -run-live -url "${DIRECT_URL:?set a controlled direct target}" \
  -interface "${TEST_INTERFACE:?set the physical interface}" -family tcp4 \
  -duration 60s -requests 1000 -interval 3s -max-bytes 1048576 \
  > "$RUN_DIR/direct4.jsonl"
python3 scripts/analyze-stability.py http "$RUN_DIR/direct4.jsonl" --minimum-samples 15
```

macOS 接口绑定使用 IP_BOUND_IF / IPV6_BOUND_IF；Linux 使用 SO_BINDTODEVICE，可能需要权限，失败会返回错误。可用 `-dns IP:PORT` 指定测试 DNS；绑定接口也应用于探针的 DNS socket。`tcp4` / `tcp6` 决定连接的地址族，不代表代理服务器自身使用同一地址族。

持续下载时，目标需要提供足够长的内容。示例上限为 64 MiB、64 KiB/s、10 分钟，不会默认选择公共下载站：

```sh
build/netprobe -run-live -url "${STREAM_URL:?set a controlled large response}" \
  -interface "$TEST_INTERFACE" -family tcp4 -stream \
  -duration 10m -requests 1 -read-rate 65536 -max-bytes 67108864 \
  > "$RUN_DIR/stream.jsonl"
python3 scripts/analyze-stability.py http "$RUN_DIR/stream.jsonl" \
  --minimum-seconds 590 --minimum-bytes 38000000
```

HTTP 测量窗口到期时，尚未收到任何响应字节（包括响应头）的请求会记录为 `worker-stop`、`stopReason=duration`，不计为成功请求、响应样本、测量时长或传输字节。只有运行中另有真实响应证据、没有其他失败且运行记录完整时，探针才可正常结束。单次请求超时、父上下文取消、提前 EOF、短响应及 HTTP 错误仍判为失败；不能仅凭错误字符串或末条记录将失败改为计划结束。分析器校验 worker-stop 的零样本形态；此记录仅补充 worker 覆盖，不能补足样本数、下载时长或最低字节数。

`--minimum-seconds` 要求至少一个实际请求持续足够长，并检查运行结束记录的时间；运行后空等不算持续传输。`--minimum-bytes` 明确最低有效传输量，避免“一字节后长时间停顿”被当成持续下载。预算过小、内容过短或样本不足返回“证据不足”。限速长响应测量的是应用接收过程，服务端可能提前发送或缓冲；要证明连接活性，应同时测定期小请求、连接复用、新连接数量及最大进度间隔，不能只比较慢速读取耗时或平均速度。

## UDP 回显与抓包

在受控 peer 上启动有时限、无放大的回显服务。地址必须是明确的 loopback、私网或文档地址，不接受通配地址。服务只回显符合协议、34..1400 字节的数据，回复字节数不超过收到的字节数：

```sh
build/netprobe -run-live -mode udp-server -address 127.0.0.1:27051 \
  -duration 30s -max-bytes 1048576
# 在另一终端；跨设备测试时改为 peer 实际可达的地址。
build/netprobe -run-live -mode udp -address 127.0.0.1:27051 -family udp4 \
  -duration 20s -workers 4 -packets 20 -interval 50ms -udp-timeout 500ms \
  > "$RUN_DIR/udp.jsonl"
```

UDP 一次运行最多 32 个 worker、合计 20000 个报文，支持 udp4/udp6。完整回复必须与当前请求逐字节一致；丢失的 sequence 和内容/顺序异常分开记录。报文包含 `cdx-udp-v1`、32 位 worker、32 位 sequence（网络字节序）及 16 字节随机 run nonce，避免将不同轮次的包错配。

在有权限的受控设备上抓取专用端口，建议 snap length 至少 160，保存 tcpdump 最终的 kernel-drop 计数；使用 `-p` 避免启用混杂模式。抓包需独立的终止/清理计时器，不依赖 SSH 会话存活。分析示例：

```sh
python3 scripts/analyze-stability.py pcap --client-log "$RUN_DIR/udp.jsonl" \
  --capture "$RUN_DIR/udp.pcap" --peer-ip 192.0.2.6 --peer-port 27051 \
  --capture-drops 0
```

只有实际记录到 kernel-drop=0 才填写 `--capture-drops 0`，未知时省略。支持 classic PCAP 的 Ethernet/VLAN、Linux SLL/SLL2、IPv4 和 IPv6 常用扩展头；不支持 PCAPNG。捕获损坏、身份截断、未解析分片或客户端/抓包矛盾时，不允许认定丢失发生在入口前。分析区分：未在此入口捕获、入口有请求但无回复、回复已捕获但客户端未收到；**入口缺少某个包不能单独证明是哪一段无线、OS 或驱动导致。**

## 实机前置检查与恢复保护

1. 记录型号/固件、Git 提交、核心 SHA-256、模式、接管范围、核心 PID、配置/选择哈希、默认网关与测试接口。检查 SSH 可达、时间、剩余内存/闪存及原有健康回退工作正常。
2. 不同流量目标必须通过核心连接信息或等效证据确认实际为 DIRECT / 代理，不能只凭域名推断。局域网对照需独立 peer；只有一个物理客户端时应注明覆盖不足。
3. 任何切换代理接管的实验，先建立唯一 owner 的租约守护和独立总时限。不得重用活动 owner；过期遗留标记应先人工核实，不自动删除接管下一轮。
4. 提前实测失联回退及恢复后的联网，再开始压力测试。默认只保留当前路由；不要在唯一上网链路上顺便刷机、改 Wi-Fi/WAN 或冷重启。
5. 每阶段记录预期/实际代理状态。核心正常但接管意外关闭，也必须停止阶段并记为失败，不能继续把直连结果当作代理稳定性。

`scripts/stability-guard.sh` 是独立诊断组件，正常安装器不会安装或启动它。需要已存在的私有运行目录、匹配的 `stability-token`，以及可用的 cleanup 脚本。guard 使用本机 uptime，不能用 Mac 的时钟替代路由器的 uptime。

在路由器上显式运行的例子（文件先由操作者传入并核对）：

```sh
TEST_OWNER=$(cat /proc/sys/kernel/random/uuid)
umask 077
# noclobber 原子拒绝覆盖活动 owner；不要自动删除已有标记。
(set -C; printf '%s\n' "$TEST_OWNER" > /tmp/codex-mihomo/stability-token) || exit 1
cut -d . -f 1 /proc/uptime > /tmp/codex-mihomo/stability-lease.next && \
  mv /tmp/codex-mihomo/stability-lease.next /tmp/codex-mihomo/stability-lease || exit 1
(trap '' HUP; exec sh /tmp/stability-guard.sh --run-live --run-dir /tmp/codex-mihomo \
  --token "$TEST_OWNER" --cleanup-script /data/codex-router/routerctl.sh \
  --lease-seconds 120 --max-seconds 2700 --poll-seconds 10) \
  >> /tmp/codex-mihomo/stability-guard-process.log 2>&1 </dev/null &
GUARD_PID=$!
kill -0 "$GUARD_PID" || exit 1
```

守护忽略 HUP，但仍必须脱离 SSH 会话并重定向全部标准流，不能只在 SSH 前台启动。启动后验证存活与实际过期回退；首次心跳写入或启动失败时不要开始实验，应核实并清理自己创建的标记。

控制器应在该设备上原子发布心跳：先写同目录临时文件，再 `mv` 替换 `stability-lease`，不要直接截断目标文件。Linux/Python 环境也可使用 `scripts/stability-heartbeat.py --run-live --run-dir DIR --token OWNER`。空值、格式错误、未来或倒退的心跳不能延长已知租约；总时限独立生效。guard 只在 owner 仍匹配时调用 cleanup 的 `disable`，cleanup 失败返回非零并保留证据。控制器还需要单实例约束；owner 检查不是对恶意并发控制器的锁。正常结束时先恢复并检查原意状态，再核对 owner 后移除自己的标记，等待 guard 退出；不能用杀掉守护来代替清理。

## 阶段、判定与记录

- 基线：关闭/开启接管做交错 A/B（至少 ABBA），使用相同接口、内容、地址族与连接条件；区分冷连接、复用连接及同一时段的对照。公网单次测速不证明固定性能上限。
- 持续连接：DIRECT 与代理并行，至少 10 分钟；重复小请求与单个长响应分别计数，不能把主动重连算作原连接未断。
- 压力：先 1+1，再 2+2、4+4，各有独立时限与全局字节上限；用受控服务并记录实际吞吐及总流量。收到错误或资源越界时先停止自己的负载，不继续升档。
- 资源：采样间隔 5 秒左右，记录实际时间差、各核 CPU/softirq、MemAvailable、Mihomo RSS、conntrack、接口错误、softnet 和 UDP 缓冲/校验和错误增量。累计错误值非零不等于本轮产生错误。
- 停止阈值在测试前确定，例如可用内存低于 60 MiB 或总 CPU 超过 88% 连续三次。还应限定连续联网/管理失败次数；不得跑完再选择有利阈值。
- 卸载观察至少 60 秒，核对预期状态、配置/选择哈希、PID/核心字节、临时地址/规则/peer/抓包是否清理。保留失败证据，按原意恢复接管；安全回退触发后不盲目自动重新开启。

分析器退出码：**0=对应证据通过，1=确认失败，2=证据不足或格式无效**。缺失结束记录、缺少 worker、截断 JSON、样本不足或未测满时长都不能算通过。P95 使用 nearest-rank，`transferredMbps` 是按请求耗时加权的传输速率，不是并发链路总吞吐；链路速率必须使用同一实际墙钟窗口的总字节数计算。性能结论需足量样本、预先约定的退化容忍值及波动范围，不能把“没有 HTTP 错误”写成“没有性能下降”。

2026-11-09 的探索实测发现过 UDP 压力丢包；当前仓库中的规范和 CI 不会把它自动改判为已解决，也不代表全部实机场景已按新规范重新验收。实际应用与冷启动结果仍见 [验证记录](validation.md)。
