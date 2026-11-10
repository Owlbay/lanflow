# 澜流 Lanflow

基于 MyGo 原生 UI 的桌面代理客户端，内核为 Mihomo（Clash.Meta），支持**系统代理**与 **TUN 模式**，macOS + Windows。

界面与功能见 [设计文档](docs/design.md)，可交互原型见 [docs/prototype/index.html](docs/prototype/index.html)。

## 安装

从 [Releases](https://github.com/Owlbay/lanflow/releases) 下载：

- **macOS**：Apple 芯片选 `lanflow-<版本>-arm64.dmg`，Intel 选 `-amd64.dmg`，把 Lanflow 拖到「应用程序」。安装包未经 Apple 签名公证，首次打开会被拦截：在「系统设置 › 隐私与安全性」里点「仍要打开」，或在终端执行 `xattr -dr com.apple.quarantine /Applications/Lanflow.app`。
- **Windows**：运行 `lanflow-setup-<版本>-amd64.exe`。安装程序未签名，SmartScreen 提示时点「更多信息 › 仍要运行」。

### 验证下载

每个 Release 附带 `SHA256SUMS.txt`。把它和安装包放在同一目录后：

```sh
shasum -a 256 -c SHA256SUMS.txt --ignore-missing     # macOS
sha256sum -c SHA256SUMS.txt --ignore-missing         # Linux、Git Bash
```

Windows PowerShell 可用 `Get-FileHash .\lanflow-setup-<版本>-amd64.exe -Algorithm SHA256` 与 `SHA256SUMS.txt` 中的值对照。

安装包由 GitHub Actions 构建并带有构建来源证明，可用 [GitHub CLI](https://cli.github.com/) 确认它出自本仓库的发布流程：

```sh
gh attestation verify lanflow-<版本>-arm64.dmg --repo Owlbay/lanflow
```

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
- **设置**：常规（快捷键、开机启动、配置同步、WebDAV 备份）、代理、DNS、嗅探（跟随订阅 / 由澜流接管）、覆写与运行时配置、内核与外部资源、高级（独立端口、局域网访问控制与认证、TUN 细项、内核行为、DNS 与嗅探的其余字段）
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
| `internal/rpc`、`internal/app/remote` | 本机接口（JSON-RPC 服务端、权限分级、审计与撤销）与生成的远程客户端、方法清单 |
| `internal/ctl`、`internal/mcp`、`skills/lanflow` | `lanflow ctl`、`lanflow mcp` 与配套的 Claude skill |
| `scripts/` | 下载内核、准备打包资源 |

技术方案见 [architecture.md](docs/architecture.md)、[advanced.md](docs/advanced.md)、[exit-map.md](docs/exit-map.md)。

## 本地开发

需要 Go 1.27.3+。先下载官方 Mihomo（固定 v1.19.33）与地理数据库到 `bin/`：

```sh
scripts/fetch-core.sh                 # 宿主平台；也可 scripts/fetch-core.sh windows amd64
```

脚本按 SHA-256 校验每个文件（内核用脚本内置的官方哈希，地理数据用 GitHub API 给出的 asset digest），不符即退出。内核写到 `bin/<goos>-<goarch>/`，宿主平台另复制一份到 `bin/mihomo`；地理数据写到 `bin/geo/`。网络受限时可设 `GITHUB_MIRROR`，镜像下载的文件同样要通过校验。

运行（真实服务 / 模拟数据）：

```sh
go tool mygo dev
LANFLOW_FAKE=1 go tool mygo dev
```

测试与检查（与 CI 相同）：

```sh
go vet ./... && go tool mygo vet .
CGO_ENABLED=0 go test ./...
go test -race ./...                   # 需要 cgo（macOS 自带的工具链即可）
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go mod tidy -diff
```

`internal/app`、`internal/core` 中的端到端测试使用 `bin/mihomo`，`internal/locate` 的部分测试使用 `bin/geo/`；没有这些文件时自动跳过，其余测试不需要网络。端到端测试不会改动系统代理；改动真实系统设置的测试需显式开启（见下文 `LANFLOW_LIVE_SYSPROXY`，以及 `scripts/vm-e2e.sh` 在虚拟机里运行的 `LANFLOW_LIVE_E2E`）。

CI（`.github/workflows/ci.yml`）在 PR 和推送到 main 时于 macOS、Windows 上运行以上检查（`-race` 只在 macOS 上跑），并先用 `fetch-core.sh` 下载内核以运行端到端测试；仓库变量 `CI_SKIP_CORE=1` 可跳过下载。

## 打包

```sh
scripts/prepare-resources.sh          # 内核、权限组件、地理数据库 → resources/
go tool mygo build                    # build/<平台>/Lanflow.app 与 .dmg
```

Windows：`scripts/prepare-resources.sh windows amd64` 后在 Windows 上执行 `go tool mygo build`。

### 发布

把 `mygo.json` 的 `version` 改为新版本号并提交，然后推送同名 tag：

```sh
git tag v0.1.0 && git push origin v0.1.0
```

GitHub Actions（`.github/workflows/release.yml`）会校验 tag 与版本号一致，在 macOS 和 Windows 上运行 vet 与测试，构建 macOS arm64 / amd64 的 `.dmg` 和 Windows 安装程序，附上 SBOM（`lanflow-<tag>.spdx.json`）和各二进制的 `go version -m` 构建信息，生成 `SHA256SUMS.txt` 与构建来源证明（attestation），并创建带自动更新说明的 Release。带 `-` 的 tag（如 `v0.2.0-beta.1`）发布为预发布版。发布流程不使用 Go 构建缓存，所用 action 固定到 commit SHA，由 Dependabot 更新。

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

## 配置同步

「设置 › 常规 › 配置同步」把设置、覆写、自定义规则和订阅保存为一个声明式文件 `lanflow.yaml`（`format: 1`，结构见 [`docs/schema/lanflow.schema.json`](docs/schema/lanflow.schema.json)），放进 iCloud 云盘（macOS）或任意同步盘文件夹（OneDrive、坚果云、Dropbox……），多台设备共用：

- 文件变化时澜流会校验（含 `mihomo -t`）后热加载，按改动热更新、重载或重启内核；不合法的修改被拒绝，配置保持原状，设置页和概览页给出原因与行号。本机自己的写入按内容哈希识别，不会被当成外部修改。
- 订阅链接和 WebDAV 密码只以 `keychain:<id>` 引用写进文件，值存系统钥匙串（macOS Keychain / Windows 凭据管理器）。另一台设备的钥匙串里没有时，订阅标为「需要重新输入链接」，不影响其余配置。目前不能借助 iCloud 钥匙串同步这些条目（需要团队签名的应用权限），原因见文档。
- TUN、内核路径、网卡名、界面状态、agent 授权等本机项不同步；缓存、统计、`runtime.yaml` 等运行时数据一直留在本机。
- 开启时若文件夹里已有不同的配置，会让你选择用文件还是用本机配置覆盖；冲突副本（如 `lanflow 2.yaml`）只提示、由你选择保留哪个，不自动删除。
- 导出 / 导入与 WebDAV 备份也改用这个格式，旧版本导出的 JSON 和 `.json` 备份仍可导入。

agent 可以直接编辑 `lanflow.yaml`，或用 `lanflow ctl config sync apply <文件> --dry-run` 预览差异；`lanflow ctl config sync status` 查看同步状态。详见 [docs/config-sync.md](docs/config-sync.md)。

## 用 agent 管理澜流

澜流运行时在本机提供一个只有当前用户能连接的接口（macOS `~/Library/Application Support/Lanflow/run/lanflow.sock`，Windows 命名管道 `\\.\pipe\lanflow-<用户 SID>`），不开 TCP 端口。同一个可执行文件提供两个客户端：

- **`lanflow mcp`**：stdio MCP 服务，供 Claude Desktop、Claude Code 等使用。在「设置 › 高级 › Agent 接入」点「复制 MCP 配置」，粘贴到 `claude_desktop_config.json` 或项目的 `.mcp.json`；或复制 `claude mcp add` 命令在终端运行。macOS 上的配置形如：

  ```json
  {
    "mcpServers": {
      "lanflow": {
        "command": "/Applications/Lanflow.app/Contents/MacOS/Lanflow",
        "args": ["mcp"]
      }
    }
  }
  ```

  工具由接口的方法清单生成，另有组合工具 `route_domain`（让域名走某个节点或代理组：规则测试 → 生成规则 → 预览 → 应用 → 验证）、`diagnose_site`、`diagnose`、`switch_node`。仓库里的 [`skills/lanflow/SKILL.md`](skills/lanflow/SKILL.md) 是配套的 Claude skill（标准流程、常见请求、禁区）。
- **`lanflow ctl <命令>`**：命令行，默认输出 JSON，`--format table` 给人看：`status`、`proxies list|select|test`、`mode set`、`sub list|add|use|update`、`rule add|test`、`config get|set|apply`（`--dry-run` 输出差异）、`config sync status|apply`、`diag`、`undo`、`audit`。macOS 上可以 `ln -s /Applications/Lanflow.app/Contents/MacOS/Lanflow /usr/local/bin/lanflow`。澜流没运行时以退出码 3 报错。

权限分级（`docs/service-split.md` §4）：

- 读取状态、节点、规则、设置随时可用；返回的设置和订阅都经过与诊断导出相同的脱敏，订阅链接不会出现。
- 连接明细、日志原文、订阅原文、运行时配置属于敏感读取：MCP 每次会话需在澜流里确认；ctl 需加 `--reveal` 并确认。
- 修改配置时，每个 MCP 客户端第一次写入会在澜流里弹窗（同时发系统通知）请你选择「仅本次 / 始终允许 / 拒绝」，结果可在「设置 › 高级 › Agent 接入」撤销。ctl 由你本人在终端执行，不再询问。
- 安装权限组件、切换内核、删除订阅、导出/恢复备份等危险操作不向 MCP 开放；ctl 每次都要在澜流里确认。
- 每次写入都记入日志目录下的 `audit.log`（参数已脱敏）；`lanflow ctl undo`（或 MCP 的 `undo`）撤销本次运行中最近一次写入，覆盖设置、自定义规则、覆写、节点选择和当前订阅。

开发时可以用模拟后端试：`LANFLOW_FAKE=1 LANFLOW_SOCKET=/tmp/lf.sock go run .`，另开终端 `LANFLOW_SOCKET=/tmp/lf.sock go run . ctl status`。

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

澜流本身以 [MIT 许可证](LICENSE) 发布。

Mihomo 为 GPL-3.0，随包分发时附带其许可证与源码信息。地图陆地数据来自 Natural Earth（公有领域）；ASN 数据库来自 MaxMind GeoLite2，需注明出处。

参考：[MyGo](https://github.com/egoist/mygo)、[Mihomo](https://github.com/MetaCubeX/mihomo)、[Mihomo API](https://wiki.metacubex.one/api/)。

## 致谢

澜流的灵感来自 Clash Party。所有代理能力都直接基于 Mihomo 内核的配置与接口实现，代码为本项目独立编写。
