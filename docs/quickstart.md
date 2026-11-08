# 安装、配对与恢复

## 前提

- 小米 BE3600 Pro，硬件 RN01，原厂固件 1.0.74；默认路由器地址 192.168.31.1。
- 电脑连接该路由器的家庭 LAN；管理员知道自己的原厂管理密码。
- 路由器已经通过原厂功能联网校时。初次生成证书前，时间必须正确。
- 电脑具备 Python 3.9+、OpenSSH 客户端；构建管理服务需要 Go 1.27。
- 导出你自己的完整 Clash/Mihomo JSON/YAML 配置。当前准备工具需要 1–2000 个内联 `proxies` 节点；仅含远程 proxy-provider 的配置需要先导出包含节点的完整配置。
- 若配置引用 rule-provider，需要同时保存它们的缓存文件，路径与导出配置中的相对路径对应。

## 1. 检查与 SSH

在仓库根目录：

```sh
python3 scripts/router-access.py
python3 scripts/router-access.py --unlock-ssh
ssh -F private/ssh/config be3600pro
```

第一条只读检查型号和固件。第二条隐藏输入原厂管理密码，生成电脑自己的 RSA 密钥，在 LAN `192.168.31.1:2222` 启动只接受密钥的 Dropbear。管理员密码不保存；临时管理员会话保存在权限 0600 的 `private/ssh/admin-session.json`，也应当保密。

首次 SSH 主机身份使用标准的首次信任方式保存至 `private/ssh/known_hosts`；后续连接严格检查，不会静默覆盖变化的主机密钥。已有授权密钥保留，首次修改前保存原文件。不会修改 root 密码或 `ssh_en` nvram 标志。

部分原厂 MAC 管理请求会执行但不及时返回 HTTP 回执。工具不将超时直接视为成功：必须进一步通过电脑自己的密钥建立 SSH，回读诊断参数确认未改变，并检查临时 MAC 未残留在相关策略配置中。验证失败时停止。

SSH 在安装启动入口前只保证本次开机可用。不要关闭 SSH 后依赖未安装的自动恢复。

## 2. 准备自己的配置

```sh
python3 -m venv work/venv
. work/venv/bin/activate
pip install -r requirements.txt
python3 scripts/prepare-config.py --config /absolute/path/to/export.yaml --rule-cache /absolute/path/to/cache
```

没有 rule-provider 时可省略 `--rule-cache`。生成文件位于 `private/`，包括新生成的管理令牌、代理认证信息和配置；重复生成会保留该目录原有的凭据。工具保留原分流规则，不替换为订阅商的整套系统配置。

若想保留电脑端手动选择的节点，可显式提供本机 Mihomo Unix 控制 socket：`--selection-socket /path/to/controller.sock`。这一步是可选的，没有任何固定用户路径。

## 3. 构建/恢复程序

```sh
sh scripts/build-agent.sh
python3 scripts/download-extensions.py
python3 scripts/restore-core-host.py
```

`restore-core-host.py` 下载校验过的官方 ARMv7 基础内核，应用仓库内的 BSDIFF 差分，然后验证最终二进制和 Android 恢复压缩包。需要 `bsdiff4`。如果已有官方基础二进制，可使用 `--base-binary /path/to/mihomo`；文件仍必须匹配固定 SHA-256。

恢复压缩包使用固定 gzip 参数。如果所用 Python/zlib 环境产生不同字节，工具会拒绝不匹配的恢复包；应使用相应发布版本提供、且匹配清单的恢复文件，或按开发说明构建自己的完整发行版本，不能跳过校验。

## 4. 备份与安装

```sh
python3 scripts/install.py
python3 scripts/install.py --install
```

第一条显示本地安装计划，不写路由器。第二条核对硬件、时间、空间和现有安装，先把原厂 firewall/DHCP 配置备份到电脑 `private/install/`，再部署。内核压缩包仅暂存在 RAM，持久闪存保留至少 1 MiB 余量。

安装器会拒绝覆盖已有配置、管理令牌或 `/tmp/mihomo`。这是初次安装工具，不是覆盖升级工具。安装失败会撤掉接管并停止本项目服务，保留备份和私有配置供排查。

首次安装后代理关闭。管理服务通过 `192.168.31.1:9443` 提供 HTTPS，配对文件下载到 `private/router-pairing.json`。安装器只增加本项目的 firewall include，不改 WAN、Wi-Fi、DHCP 地址或原厂固件。

## 5. Android App

从可信发布版本取得 APK，或按开发说明本地构建。安装后，把 `private/router-pairing.json` 通过你信任的方式传入手机，在 App 中“导入配对文件”。配对成功后可删除手机下载目录里的明文副本，App 将凭据加密保存在 Android Keystore 保护的存储中。

- **状态**：代理开关和规则/全局/全部直连模式。
- **节点**：选择手动策略组的节点。
- **订阅**：显示已有配置内联节点；支持另外添加、更新和删除可管理的订阅来源。完整配置导入的节点无法反推出原订阅链接；需要提供自己的原链接后才能管理该来源的更新。
- **设置 → 直连设备**：统一设备列表多选，一次保存；下次开启代理生效。
- **离线恢复核心**：通过家庭 Wi-Fi 上传 App 内置恢复包，无需路由器从公网下载。

开启接管时会解析配置及已管理订阅中的普通 `server`/`port` 节点端点，并只对这些地址与端口保留原生直连，避免终端自己的代理与路由器叠加。解析使用原厂系统 DNS，失败的地址不会被凭空加入；节点地址或来源变化后，重新开启代理会刷新这份策略。端口跳跃、多 peer 等特殊节点配置尚未单独验证。它不等同于把终端整机加入直连设备。

## 6. 故障恢复

先在 App 点“关闭代理”。如果 App 不可达，用 SSH：

```sh
ssh -F private/ssh/config be3600pro 'sh /data/codex-router/routerctl.sh disable'
```

这会撤掉本项目的 IPv4/IPv6 透明接管、DNS 接管和策略路由，恢复保存的桥接参数，并保存关闭意图。核心或管理服务健康检查连续失败时，看门狗也会走这条路径。

如果提示“配置恢复未完成”，先保持代理关闭，再重启管理服务以触发事务日志恢复：

```sh
ssh -F private/ssh/config be3600pro 'sh /etc/rc.common /data/codex-router/agent.init restart'
```

这里重启的是本项目管理服务，不是路由器。恢复会先停止旧核心，恢复上一份配置后再启动，代理保持关闭供检查。

重启后，管理服务应先恢复；内核自动下载或差分恢复失败时，网络保持普通直连，在 App 使用离线恢复。**这条重启流程尚待实际重启验收，不应当在无法物理接触路由器时依赖它作为唯一恢复手段。**

只更新管理程序时，先上传新压缩包与 `scripts/update-agent.sh` 到路由器 `/tmp`，再执行：

```sh
sh /tmp/update-agent.sh /tmp/router-agent.gz SHA256_OF_THE_COMPRESSED_ARCHIVE
```

更新工具保留 RAM 回退副本，验证本机 HTTPS 健康状态及配置未变化，不操作 Mihomo 转发规则。不要把整个 `private/` 上传到公开仓库。

## 7. 卸载服务并保留配置

把 `scripts/uninstall.sh` 上传到路由器 `/tmp` 后执行：

```sh
sh /tmp/uninstall.sh --remove-service
```

它停止本项目服务、撤掉代理并删除本项目的启动 include，保留 `/data/codex-router` 中的配置、备份和当前 SSH。不会清空其他人的 SSH 授权密钥。当前维护 SSH 不会通过已删除的启动入口在下次开机自动恢复。
