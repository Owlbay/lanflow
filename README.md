# 澜流 Lanflow

基于 MyGo 原生 UI 的桌面代理客户端，内核为 Mihomo（Clash.Meta），支持**系统代理**与 **TUN 模式**，macOS + Windows。

界面与功能见 [设计文档](docs/design.md)，可交互原型见 [docs/prototype/index.html](docs/prototype/index.html)。

## 功能

- **开关与模式**：内核、系统代理（全局或 PAC）、TUN 三个独立开关，规则 / 全局 / 直连；TUN 通过权限组件 `lanflow-helper` 运行，应用本身不提权，IPv4 与 IPv6 一并接管
- **概览**：可拖动旋转、缩放的 3D 地球出口图（请求流向、实时速度、自动聚焦）、订阅用量、实时速度、终端代理命令
- **代理与订阅**：代理组、节点切换、测速（可设测速地址与超时）、组图标、定位当前节点；订阅链接 / 文件 / 拖入导入、自动更新、按 Wi-Fi 自动切换订阅；多个有序 YAML 覆写（本地或链接）
- **连接**：列表与网络拓扑两种视图（来源 → 进程 → 规则 → 代理组 → 节点 → 目标），应用图标，从连接添加规则
- **日志**：内核日志与澜流运行日志，级别筛选、搜索；一键导出脱敏的诊断信息
- **规则**：自定义规则、命中统计、临时停用、规则测试、规则集
- **网络**：直连与代理出口识别、网站连通性、DNS 查询、泄漏检查（DNS / IPv6 / WebRTC / IP 类型 / DNS 对比）
- **统计**：按设备、域名、进程、规则、代理、地区、入站统计请求数与上下行流量，按天保存
- **检测**：出口 IP 纯净度、常见服务解锁检测、按地区探查节点
- **内核**：Mihomo 正式版 / 预发布多版本并存，一键切换与回滚，检查更新，下载镜像
- **设置**：常规（快捷键、开机启动、WebDAV 备份）、代理、DNS、嗅探（跟随订阅 / 由澜流接管）、覆写与运行时配置、内核与外部资源、高级（独立端口、局域网访问控制与认证、TUN 细项、内核行为、DNS 与嗅探的其余字段）
- **共存**：与其他代理软件共存，深信服 aTrust 兼容模式
- 托盘菜单、全局快捷键、单实例、关闭到托盘、异常退出后的系统代理恢复、内核崩溃自动重启

## 目录

| 目录 | 职责 |
| --- | --- |
| `main.go` | 入口：单实例、窗口、托盘、退出清理 |
| `internal/model` | 共享数据类型 |
| `internal/app` | 服务层（`app.API` 的实现）；`app/fake` 为界面开发用的模拟数据 |
| `internal/core` | Mihomo 进程管理（unix socket / 命名管道控制，无 TCP 端口） |
| `internal/controller` | Mihomo REST / WebSocket 客户端 |
| `internal/profile` | 订阅存储与下载、覆写合并、运行时配置合成 |
| `internal/platform/sysproxy`、`netinfo` | 系统代理、本机网络信息 |
| `internal/platform/helper`、`cmd/lanflow-helper` | TUN 权限组件（macOS LaunchDaemon / Windows 服务） |
| `internal/geo`、`internal/locate` | 节点名识别、Cloudflare trace 实测、中转分析、陆地点阵 |
| `internal/view/...` | 界面：外壳与各页面 |
| `scripts/` | 下载内核、准备打包资源 |

技术方案见 [architecture.md](docs/architecture.md)、[advanced.md](docs/advanced.md)、[exit-map.md](docs/exit-map.md)。

## 本地开发

需要 Go 1.27.3+。先下载官方 Mihomo（固定 v1.19.33）与地理数据库到 `bin/`：

```sh
scripts/fetch-core.sh
```

运行（真实服务 / 模拟数据）：

```sh
go tool mygo dev
LANFLOW_FAKE=1 go tool mygo dev
```

测试（含使用 `bin/mihomo` 的端到端测试，不会改动系统代理）：

```sh
CGO_ENABLED=0 go test ./...
go vet ./... && go tool mygo vet .
```

## 打包

```sh
scripts/prepare-resources.sh          # 内核、权限组件、地理数据库 → resources/
go tool mygo build                    # build/<平台>/Lanflow.app 与 .dmg
```

Windows：`scripts/prepare-resources.sh windows amd64` 后在 Windows 上执行 `go tool mygo build`。正式签名、公证和发布标识（目前为占位的 `com.example.lanflow`）在发布阶段配置。

### 应用自动更新

澜流用 MyGo 的签名更新（`mygo.Updater`）：「设置 › 常规 › 关于」里的「检查更新」和「下载并重启」，以及每天启动时的一次自动检查（可在同一处关闭）。`mygo.json` 目前**没有** `updates` 块，所以当前构建会显示「此版本未配置更新源」，不会报错。接入更新源只需三步，代码不用改：

1. 生成签名密钥（只做一次）：

   ```sh
   go tool mygo keygen        # 写出 mygo-update.key（私钥）和 mygo-update.pub（公钥），并打印位置
   ```

   私钥**不要**放进仓库：保存在密码管理器，并作为 CI 的 secret（`MYGO_UPDATER_PRIVATE_KEY`）。已安装的应用只接受用它签名的更新，丢了就无法再推送更新，泄露则任何人都能向用户下发代码。

2. 在 `mygo.json` 里加上公钥和发布位置，二选一：

   ```json
   {
     "version": "0.2.0",
     "updates": {
       "publicKey": "<mygo-update.pub 的内容>",
       "github": "<owner>/<repo>"
     }
   }
   ```

   `github` 指向一个公开仓库，发布版 tag 为 `v0.2.0` 这样的形式（`mygo build -upload` 会上传到该版本的草稿 release，发布后即生效）。用自己的 HTTPS 服务器或 S3/R2 时改为 `"url": "https://downloads.example.com/lanflow"`，并把构建产物（`*.tar.gz`、`*.delta`、`update-<平台>.json`）上传到该目录，清单文件最后上传。

3. 构建时提供私钥，`mygo build` 会为每个平台写出签名清单与增量更新包：

   ```sh
   MYGO_UPDATER_PRIVATE_KEY="$(cat /path/to/mygo-update.key)" go tool mygo build
   ```

   更新日志取自 `CHANGELOG.md` 中对应版本的 `## 0.2.0` 小节（文件存在时必须有该小节）。开发构建（`mygo dev`、`go run`）和由包管理器安装的副本不会自更新。详见 MyGo 文档 `docs/updates.md`。

## 日志与诊断

- **澜流运行日志** `lanflow.log`（macOS `~/Library/Logs/Lanflow/`，Windows `%LOCALAPPDATA%\Lanflow\Logs\`，满 5 MB 轮转，保留 1 份）：记录内核启停、重载和崩溃，系统代理、TUN 和模式切换，权限组件安装，订阅导入、更新和自动更新，覆写保存，设置改动（只记字段名），系统 DNS 的设置与恢复，aTrust 检测结果，以及界面上弹出过的每条提示和错误。写入前会把链接改成只保留域名，并给 token、password 等值打码。
- **内核日志** `mihomo.log`（同目录，5 MB 轮转）；TUN 模式下由权限组件运行的内核写 `<数据目录>/core/mihomo-tun.log`；权限组件的日志在 `/Library/Logs/Lanflow Helper/`。
- **日志页**：可在「内核」和「澜流」两种日志之间切换，支持级别筛选、搜索、暂停、复制，也可在访达中显示对应文件。要看 DNS 解析过程，把内核日志级别调到「调试」后搜索 `dns`。
- **导出诊断信息**（日志页 ⋯ 菜单，或「设置 › 常规」）：打包成 ZIP，包含上述日志（每个文件最多取最后 2 MB）、运行时配置和覆写（只保留节点名称和类型，删去服务器地址、UUID、密码、密钥和传输参数）、设置、状态摘要、订阅列表（只保留域名），以及系统路由表、DNS 解析器和系统代理设置。内核日志里仍有访问过的域名，分享前请自行检查。

## 与其他代理软件共存

- 代理端口被占用时不会悄悄运行：首次运行若 7890 已被占用会自动选下一个空闲端口；之后改到被占用的端口或启动时端口被占用，都会明确报错。
- 开启系统代理前会保存原设置（包括其他软件设置的代理和绕过列表），关闭时若仍指向澜流就原样恢复；若已被其他软件改过则不去动。
- 检测到其他软件的 TUN 正在接管默认路由时，拒绝开启澜流的 TUN 并说明原因。
- macOS 开启 TUN 时默认把系统 DNS 指向 223.5.5.5、119.29.29.29（否则发往路由器等局域网 DNS 的查询不经过 TUN），关闭 TUN、停止内核或退出时恢复原设置；期间被用户改过的网络服务不会被覆盖，异常退出后下次启动时恢复。可在「设置 › 代理」中关闭。
- **aTrust 兼容**（「设置 › 代理」，默认开启）：检测到深信服 aTrust 运行时（进程名含 aTrust / Sangfor），读取它的隧道路由（`netstat -rn`）、内网 DNS 域名和解析器（`scutil --dns`）以及它自身连接的网关（`netstat -anv`），然后：内网网段和网关写入 `tun.route-exclude-address`，不进入澜流的 TUN（内核重启也不会打断 aTrust 隧道）；含有 DNS 服务器（系统 DNS、aTrust 的解析器或配置的公司 DNS）的网段**不会**被排除，只直连并仍经过 TUN，否则 DNS 劫持失效、被墙网站全挂；aTrust 进程、内网网段、内网域名作为最前面的 DIRECT 规则；内网域名加入 `fake-ip-filter` 和嗅探的 `skip-domain`，并通过 `nameserver-policy` 交给公司 DNS 解析；aTrust 在线时加密 DNS 常被掐断，因此追加 223.5.5.5 / 119.29.29.29 纯 UDP 上游兜底；fake-ip 网段或 TUN 地址与内网重叠时自动换用其他网段；澜流的 TUN 固定命名为 `utun1700`，与 aTrust 的 utun 区分；系统代理的例外列表加入内网。「自动设置系统 DNS」保持开启。每 10 秒重新检测，aTrust 连上、断开或退出后自动重新生成配置。这些规则在「运行时配置」里标为「aTrust 兼容」。
  - **配置**（「设置 › 代理 › aTrust 兼容 › 配置…」）：检测不到的部分按行填写——内网域名（走公司 DNS）、直连域名（钉钉、企业微信等公网 SaaS，直连但绝不走公司 DNS）、公司 DNS、不进入 TUN 的网段（aTrust 映射段、只能经 aTrust 到达的内网段）、仅直连的网段（含公司 DNS 的 10.0.0.0/8 之类），以及「未运行 aTrust 时也应用」（办公网不开 aTrust 时内网域名仍需公司 DNS）。表单实时校验，含公司 DNS 的网段放进排除列表会被拦下。澜流不内置任何公司的地址：「复制参考配置」/「导出参考配置…」给出一份带中文说明、用示例值（corp.example.com、10.x、203.0.113.x）写成的 YAML 模板；「导入…」读取澜流的 YAML/JSON，或 JavaScript 覆写脚本（如 `atrust-coexist.js`）（`atrustDns`→公司 DNS、`corpDomains`→内网域名、`publicSaasDomains`→直连域名、`atrustCIDRs`→不进入 TUN 的网段、`IP-CIDR,…,DIRECT` 规则→仅直连的网段），校验去重后填入表单，确认后再保存；「导出…」写出 YAML。其他分隧道 VPN（如 Tailscale）的网段在 aTrust 运行时也会一并直连；接管全部流量的其他代理 TUN 不算在内。
- 实测系统代理：`LANFLOW_LIVE_SYSPROXY=1 go test -run LiveSystemProxy ./internal/app/`（只用直连节点，几秒后自动恢复原设置）。

## 已知限制

- Windows 版代码已交叉编译并通过静态检查，尚未在真实 Windows 上运行。
- TUN 已在 macOS 26 虚拟机中实测（`scripts/vm-tun-test.sh <虚拟机名>`）：安装权限组件、开启 TUN、无代理设置的请求被 TUN 接管、关闭后路由恢复、卸载无残留。图形界面里的管理员授权弹窗尚未实测。
- 「自动设置系统 DNS」目前只支持 macOS；Windows 上 DNS 若指向局域网地址，查询不经过 TUN，只能按 IP 和嗅探分流。
- aTrust 兼容尚未在装有 aTrust 的机器上实测；Windows 上只按进程名让 aTrust 自身流量直连，不读取它的路由和内网域名。
- 本机同时运行其他代理软件的 TUN 时，「直连」探测也会被其接管，地图上「你」的位置可能不准。
- macOS 14 起，未授予定位权限时系统隐藏 Wi-Fi 名称。

## 许可

Mihomo 为 GPL-3.0，随包分发时附带其许可证与源码信息。地图陆地数据来自 Natural Earth（公有领域）；ASN 数据库来自 MaxMind GeoLite2，需注明出处。

参考：[MyGo](https://github.com/egoist/mygo)、[Mihomo](https://github.com/MetaCubeX/mihomo)、[Mihomo API](https://wiki.metacubex.one/api/)。

## 致谢

澜流的灵感来自 Clash Party。所有代理能力都直接基于 Mihomo 内核的配置与接口实现，代码为本项目独立编写。
