# 构建与测试

## 目录

```text
agent/       Go HTTPS 管理服务、事务化配置与恢复逻辑
android/     原生 Java Android App，构建脚本与主机侧测试
scripts/     获取 SSH、准备配置、安装、更新、恢复与诊断工具
tests/       防火墙/生命周期/工具回归，TCP/UDP 实机测试 peer
patches/     Mihomo 固定上游提交上的完整源代码补丁
release/     发布清单、固定 SHA-256 和内核二进制差分
private/     使用者自己的凭据/配置/签名密钥（不进入 Git）
work/        SDK、依赖源码及临时工作区（不进入 Git）
build/       构建产物（不进入 Git）
logs/        本地原始测试记录（不进入 Git）
```

首次运行请先阅读 [安装说明](quickstart.md)。不要对其他型号或固件取消检查后直接运行安装器。

## Go 管理服务

需要 Go 1.27。可以通过环境变量 `GO` 指定工具链路径。首次拉取模块需要网络；之后可使用缓存离线构建。

```sh
sh scripts/build-agent.sh
cd agent
go test -race ./...
go vet ./...
```

输出为 `build/agent/router-agent` 与 `build/agent/router-agent.gz`，目标是 Linux ARMv7、CGO 关闭。签名密钥、订阅及配对文件均不是编译输入。

## 恢复固定版本内核

只需要准备现有发布版本时，不必重编译 Mihomo：

```sh
python3 -m venv work/venv
. work/venv/bin/activate
pip install -r requirements.txt
python3 scripts/restore-core-host.py
```

它校验官方基础压缩包、基础二进制、BSDIFF 差分、最终二进制与恢复压缩包。所有固定值见 `release/core-release.json`。差分只占约 647 KiB；完整内核仅放在 RAM。

需要重编译或修改内核时：

```sh
git clone https://github.com/MetaCubeX/mihomo.git work/mihomo-source
git -C work/mihomo-source checkout 88dcbf7f1614a67c3b36b848ee3592dfa92ada36
git -C work/mihomo-source apply ../../patches/mihomo-v1.19.32-udp-reply-candidate.patch
# 使用 Go 1.26.8，先在该源码树下载模块至与构建脚本相同的 GOPATH。
GOPATH="$PWD/work/go-path" /path/to/go1.26.8/bin/go -C work/mihomo-source mod download
GO=/path/to/go1.26.8/bin/go sh scripts/build-udp-candidate.sh
```

构建脚本默认不下载模块，固定上游提交及 Go 1.26.8，运行修改部分的 race 测试和 vet，生成 ARMv7 内核及 Linux 测试 peer。源码修改、编译器或构建选项变化都可能改变二进制；如果制作新发行版本，必须一起更新清单、BSDIFF、Go 恢复常量、shell 常量与 Android 资产，并重新验证恢复结果。不能只改一个 SHA-256 来绕过校验。

## Android App

需要 JDK 21、Android platform 35 和 build-tools 36.0.0。构建采用原生 Java、aapt2、D8 与 apksigner，不需要 Gradle。

```sh
export JAVA_HOME=/path/to/jdk-21
export ANDROID_SDK_ROOT=/path/to/android-sdk
bash android/build.sh
```

构建前先生成匹配清单的 `build/release/mihomo-router-armv7.gz`。脚本会验证恢复资产与源码补丁、运行主机侧 Java/资产测试，签名后检查 APK 的签名、ZIP CRC、资源表存储方式和 4 字节对齐。

第一次构建会在 `private/` 生成本地签名密钥与密码文件。升级同一个安装必须继续使用同一把签名密钥；不要提交或公开密钥。可用 `ANDROID_KEYSTORE`、`ANDROID_SIGNING_PASSFILE` 指定已有签名材料。

输出为 `build/android/router-proxy-0.2.1.apk`。App 的配对数据使用 Android Keystore 加密保存，覆盖安装应保留配对；清除数据或更换签名是另一回事。

## 离线测试

```sh
python3 -m unittest discover -s tests -p 'test_*.py' -v
python3 -m unittest discover -s android/tests -p '*_test.py' -v
```

这些测试使用临时目录和记录用的伪防火墙工具，不修改电脑或路由器防火墙。Go 服务测试在 `agent/` 中运行。`tests/udp-peer` 和 `tests/tcp-transition` 是实机 peer 源码，不会由普通 Python 单元测试自动连接路由器。

## GitHub Actions CI

`.github/workflows/ci.yml` 在推送 `main`、推送 `v*` 标签、提交 Pull Request 或手动触发时运行：

- Python 3.11 / 3.13：工具、防火墙规则和 Android 恢复资产回归。
- Go：所有三个模块的 race 测试、vet、格式检查，以及管理服务和诊断 peer 的 Linux ARMv7 编译。
- Android：JDK 21、platform 35、build-tools 36.0.0；从官方基础内核和仓库差分重建并校验恢复包，再执行完整 App 构建、主机侧测试、APK 签名和资源对齐检查。

工作流只有仓库读取权限，不需要路由器密码、配对文件、订阅或发布签名密钥，不运行连接真实路由器的诊断脚本。Actions 固定到提交 SHA。CI 通过代表主机侧检查和交叉编译通过，不能代替实机重启、透明 socket 或游戏验证。

成功的构建会保留管理服务与测试 APK 产物 14 天。APK 每次使用临时生成的签名密钥，**不能用来覆盖安装本机签名或其他 CI 运行签名的 App**；需要持续升级时应自行保存并复用签名密钥。临时密钥及其密码不会上传为产物。

## 显式实机诊断

低负载采样，不启动代理、不给 WAN 跑测速：

```sh
python3 scripts/router-metrics.py --ssh-config private/ssh/config --seconds 60 --interval 3 --output logs/metrics-new.jsonl
```

输出每核 CPU、软中断、可用内存、Mihomo 实际 RSS、接口错误/丢包增量及 TCP 重传增量。累积计数下降按复位处理，不报告负数。输出文件必须是新文件，避免覆盖旧证据。

实机管理 API 测试会切换模式、创建/更新/删除临时订阅，只允许在代理接管关闭时运行。测试节点是不可用的合成节点，不复制真实节点密码：

```sh
python3 scripts/integration-test.py --run-live --listen-ip YOUR_COMPUTER_LAN_IP
```

`YOUR_COMPUTER_LAN_IP` 必须是电脑在 `192.168.31.0/24` 的实际地址；配对和受信任证书默认来自初次安装器。测试源服务器只监听该 LAN 地址，结束时删除临时来源并恢复原模式。

TCP 切换诊断当前使用 macOS 客户端：

```sh
go -C tests/tcp-transition build -o ../../build/tcp-transition-mac .
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go -C tests/tcp-transition build -o ../../build/tcp-transition-armv7 .
python3 scripts/test-tcp-transition.py --run-live --ssh-config private/ssh/config --curl-config private/agent-curl.conf --mac YOUR_COMPUTER_WIFI_MAC --output logs/tcp-new-run
```

这个测试需要先为电脑准备含受信任证书和 Authorization 头的私有 curl 配置；它不是公开示例凭据。测试默认路由必须是被测路由器，开始时接管必须关闭。它只在路由器 loopback 临时添加两个文档专用地址，发送少量回显数据；60 秒独立清理计时器、120 秒接管回退及正常退出清理共同兜底，不使用公网测试服务器。

UDP Linux 隔离测试脚本 `scripts/test-udp-isolated-linux.sh` 仅使用明确选择的 Docker context、内部双栈网络和自建 scratch 测试镜像，禁止下载镜像；不会自动启动 VM。先构建测试 peer。没有可用 Linux 环境时，不应把主机侧编译通过写成 Linux socket 实测通过。

## 发布前

- 只提交源码、清单、完整补丁和脱敏验证记录。
- 核对 Go/shell/Android 清单一致，内核差分能重建目标字节。
- 运行单元测试、签名和资源对齐检查。
- 明确区分：离线测试、当前设备实测、恢复出厂安装、实际重启、长期负载与真实应用体验。
- 不上传 `private/`、`work/`、`build/`、`logs/`、电脑导出配置或其他人的订阅。

暂存变更后，运行 `python3 scripts/check-source.py` 检查路径、私钥块及与本地私有配置相同的凭据。可用 `--check-extra-secret` 隐藏输入额外凭据进行精确值检查，不保存输入。输出只报告位置和类别，不打印命中的凭据。这是发布前的辅助检查。
