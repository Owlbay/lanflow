# 后台服务与按需界面

状态：S1 已实施（见第 8 节），S2、S3 尚未开始。目标：界面关闭后进程真正退出、释放内存；代理、订阅更新、统计、检测和通知照常运行；`lanflow ctl`、MCP 和界面共用同一套本机接口。

## 1. 现状与实测

- 一个进程同时承载服务层（`internal/app.Service`）、界面（mygo 窗口）、托盘和全局快捷键；内核由 helper（TUN）或子进程托管，与界面无关。
- 虚拟机实测（0.1.0，运行 4 分钟后）：Footprint 170 MB。Go 堆在用 80 MB，其中约 74 MB 是界面绘制缓冲（地球描边遮罩约 50 MB，已在 `d9e18e2` 修复；文字图集 16 MB），服务层约 5 MB；窗口与 GPU 缓冲约 52 MB。
- 隐藏窗口时已停止绘制、放慢连接流、释放页面缓存（`internal/view/shell.go` `setBackground`），但窗口与 GPU 缓冲、渲染器的常驻缓冲只有退出界面进程才能释放。

结论：服务本身很轻，拆分的收益主要来自「界面不开就不占」。

## 2. 进程模型

| 进程 | 内容 | 生命周期 |
| --- | --- | --- |
| **服务进程** `Lanflow --service` | `app.Service`、托盘、全局快捷键、系统通知、本机接口服务端 | 登录时启动（沿用「开机启动」设置），常驻；退出它即退出澜流 |
| **界面进程** `Lanflow`（默认） | mygo 窗口与全部页面，经本机接口连服务 | 点托盘「打开澜流」、Dock/开始菜单图标、深链接时启动；关窗即退出 |
| 内核 | Mihomo，helper 或服务进程托管 | 不随界面开关重启，现有连接不受影响 |

- 同一个可执行文件，按参数分两种模式，打包与签名不变。
- 托盘留在服务进程：mygo 的托盘和菜单不需要窗口，服务进程不建窗口就不分配 GPU 表面。托盘里切换节点、开关代理、切换模式直接调服务，不拉起界面。
- 启动界面时如果服务没在运行（首次启动、被强杀），界面先拉起服务再连接；服务先于界面就绪，界面断线后自动重连并刷新全部快照。
- 单实例：服务进程持有现有的单实例锁；第二个界面进程只把窗口提到前台。

## 3. 本机接口

传输：macOS 用 Unix 套接字 `~/Library/Application Support/Lanflow/run/lanflow.sock`（目录 0700，套接字 0600），Windows 用命名管道 `\\.\pipe\lanflow-<用户 SID>`（DACL 只允许当前用户）。连接时校验对端（macOS `LOCAL_PEERCRED` uid，Windows 客户端 SID），沿用 helper 已有做法。不开 TCP 端口。

协议：JSON-RPC 2.0，每行一个消息。

- **请求**：方法名即 `app.API` 的方法名，参数和返回值是 `internal/model` 里的类型（本来就是可复制的快照值，JSON 可直接编码）。
- **事件**：`subscribe(topics)` 之后，服务按现有合并窗口推送 `{"method":"changed","params":{"topic":"conns"}}`，客户端收到后再拉快照，与现在 `OnChange` 的语义一致。
- **两个特殊方法**：`OnChange` 由事件流实现；`UpdateUIState(fn)` 在客户端执行 fn，再以 `PatchUIState(model.UIState)` 整体提交（UI 状态只有界面会改，冲突可忽略；需要时再做字段级补丁）。
- **可选能力**：`SetBackground`、`Resumed`、`NetworkChanged`、`AppLogWriter` 这些 `api.(interface{...})` 断言，在远程客户端里实现为对应的 RPC 或本地空操作（唤醒与网络事件由服务进程自己监听）。
- **大数据**：连接列表、日志、规则可能很大，`Connections` 等方法支持 `since`/`limit` 增量参数；第一版先整包返回，按实测再优化。

客户端：`internal/app/remote` 用 `go generate` 从 `app.API` 生成实现同一接口的客户端，界面代码不变（`main.go` 的 `backend()` 选择本地、远程或模拟后端）。生成器同时产出方法清单，供 ctl/MCP 使用并做兼容检查。

版本：握手交换协议版本和澜流版本；不一致时界面提示重启服务，不做跨版本兼容。

## 4. 权限分级（ctl / MCP / 界面）

| 级别 | 方法举例 | 界面 | `lanflow ctl` | MCP |
| --- | --- | --- | --- | --- |
| 读状态 | `Status`、`Proxies`、`Rules`、`Settings`（脱敏） | ✓ | ✓ | ✓ |
| 敏感读 | 订阅链接、连接明细、日志原文、解密内容 | ✓ | 需 `--reveal` 且界面确认 | 默认拒绝，需按会话授权 |
| 写配置 | `SaveSettings`、`SelectProxy`、`AddCustomRule`、订阅增删 | ✓ | ✓（`--dry-run` 先给 diff） | 需首次授权，每次写入记审计 |
| 危险操作 | 安装/卸载 helper、根证书、删除全部订阅、导出备份 | ✓ | 需界面确认 | 不开放 |

- 客户端在握手时声明身份（`ui`、`ctl`、`mcp:<名称>`）；服务按身份套用上表。同一用户的进程本就能读写数据目录，这里防的是 agent 越权和误操作，不是防恶意本地程序。
- 首次有新 agent 写配置时，托盘弹通知请用户允许（本次 / 始终 / 拒绝），结果存在设置里，可在「设置 › 高级」撤销。
- 审计日志 `logs/audit.log`：时间、客户端、方法、参数摘要（脱敏）、结果；`ctl undo` 用它回滚最近一次配置写入。
- 所有返回给 ctl/MCP 的设置和订阅都走诊断导出已有的脱敏逻辑（`applog.Redact`）。

## 5. ctl 与 MCP

- `lanflow ctl <命令>`：同一可执行文件的子命令，输出 JSON（`--format table` 给人看）。命令与第 4 节的方法一一对应：`status`、`proxies list|select`、`mode set`、`sub list|add|use|update`、`rule add|test`、`config get|set|apply --dry-run`、`diag`、`undo`。
- `lanflow mcp`：stdio MCP 服务，工具由方法清单生成，再手写少量组合工具（如「让某域名走某节点」= 规则测试 → 生成规则 → dry-run → 应用 → 连通性验证）。设置页提供「复制 MCP 配置」。
- Skill：随仓库提供 `skills/lanflow/SKILL.md`，讲标准流程（看状态 → 预览 → 应用 → 验证）、常见请求和禁区。

## 6. 实施顺序

| 阶段 | 内容 | 风险 | 收益 |
| --- | --- | --- | --- |
| S1 | 在现有单进程里加本机接口服务端 + 生成客户端；`lanflow ctl` 和 `lanflow mcp` 作为客户端 | 低：界面仍走进程内 API | agent 立即可用；接口经 ctl 先行实测 |
| S2 | `--service` 模式（服务 + 托盘 + 快捷键 + 通知），界面改用远程客户端；关窗退出界面进程 | 中：托盘与界面拆开、断线重连 | 关窗后释放界面内存 |
| S3 | 登录项改为启动服务进程；首次启动、升级、崩溃恢复流程；Windows 适配 | 中 | 完整的轻量常驻 |

S1 完成后再做配置同步（`lanflow.yaml` 也经同一接口加载和校验）与反馈上报（报告由服务进程生成）。

## 7. 待验证

- mygo 在没有窗口的进程里运行托盘、全局快捷键和系统通知（macOS `LSUIElement` 与 Dock 图标的处理）。
- 界面进程退出后 macOS 的 Dock 行为：服务进程不显示 Dock 图标，点「应用程序」里的 Lanflow 需要拉起界面而不是第二个服务。
- 远程客户端下各页面的刷新延迟，尤其是连接页（每秒一帧的整包传输）。
- Windows 命名管道在服务以普通用户运行、helper 以 SYSTEM 运行时的权限组合。

## 8. S1 实施说明

S1 在现有单进程里加了本机接口，界面仍直接调用进程内的 `app.Service`；`lanflow ctl` 和 `lanflow mcp` 是它的客户端。

**代码位置**

| 位置 | 内容 |
| --- | --- |
| `internal/rpc` | 协议（`protocol.go`）、传输与对端校验（`transport_*.go`、`peer_*.go`）、服务端（`server.go`）、权限（`guard.go`、`levels.go`）、审计（`audit.go`）、撤销（`undo.go`）、agent 专用方法（`agentops.go`）、脱敏（`redact.go`）、通用客户端（`client.go`）；`manifest_gen.go` 是生成的方法清单 |
| `internal/app/remote` | 生成的 `app.API` 客户端（`client_gen.go`）；生成器在 `apigen/`，`go generate ./internal/app/remote` 同时写出客户端和清单 |
| `internal/ctl`、`internal/mcp` | 两个子命令；`main_cli.go` 在单实例锁之前分派，不开窗口 |
| `main_rpc.go` | app 启动时开始服务（模拟后端同样提供），退出时关闭；授权弹窗经 `view.Shell.AskAgentConsent` |
| `internal/view/agents.go`、`settings/agents.go` | 授权弹窗；「设置 › 高级 › Agent 接入」：复制 MCP 配置、撤销授权 |
| `skills/lanflow/SKILL.md` | 配套 skill |

**与设计的差异和补充**

- 传输：socket 路径是 `<DataDir>/run/lanflow.sock`（目录必须属于当前用户，0700；socket 0600）；Windows 管道 DACL 只含当前用户（`D:P(A;;GA;;;<SID>)`），服务端与客户端都用 `GetNamedPipeClientProcessId` / `GetNamedPipeServerProcessId` 核对对端进程的用户 SID。另一个进程正在服务时 `Listen` 返回 `ErrInUse`，残留的 socket 会被替换。`LANFLOW_SOCKET` 可改端点（测试、开发）。
- 协议：参数是按声明顺序的 JSON 数组，结果是去掉 error 后的返回值数组；`hello` 必须是第一条消息，协议版本（`rpc.ProtocolVersion`）不一致即拒绝，澜流版本不一致只记日志。`app.ErrHelperRequired` 以 `data.kind = "helper-required"` 传回并在客户端还原。请求并发处理，事件在独立 goroutine 发送，慢客户端不拖住服务。
- 方法清单：`internal/rpc/levels.go` 是权限表的唯一来源，生成器把它和 `api.go` 合成 `manifest_gen.go`（名称、参数名与类型、结果类型、级别、文档）。API 新增方法而 `levels.go` 没有对应条目时，生成器和 `internal/app/remote` 的测试都会失败；生成文件过期也会被测试发现。
- 权限：在四级之外加了「仅界面」级（`TakeNotices`、UI 状态、`ResolveRecovery`、`DismissProxyHint`、`PatchUIState`），任何 agent 都不能调用。只测量、不改配置的动作（测速、检测、DNS 查询、刷新网络、泄漏检查）归为「读」。删除订阅、重置统计、切换/安装内核、备份与恢复、WebDAV 密码归为「危险」。
- 授权：MCP 首次写入、MCP 敏感读、ctl `--reveal` 敏感读、ctl 危险操作都经界面弹窗（同时发系统通知并把窗口拉到前台）。写入的「始终允许 / 拒绝」存进 `Settings.AgentGrants`，「仅本次」只对当前连接有效；敏感读只按连接授权。等待超过 2 分钟视为拒绝。没有界面可问时（`Prompt` 为空）一律拒绝并说明原因。
- 整体写设置：agent 读到的设置已脱敏，整体写回会把掩码存进去，所以 `SaveSettings`、`ApplyDNS`、`ApplySniffer` 对 agent 拒绝，改用新增的 `PatchSettings(patch, dryRun)`：按字段名（不区分大小写）合并 JSON 补丁，未知字段报错，`UI`、`AgentGrants`、`CorePath`、`CoreVersion`、`WebDAV`、`ConnHiddenColumns` 不可改；`dryRun` 返回脱敏后设置的行级差异（前后各 2 行上下文）。`EditSubscription` 收到空链接时保留原链接。
- 其他 agent 方法：`Undo`、`AuditLog(n)`、`Methods`（清单，供兼容检查）、`PatchUIState`（`UpdateUIState` 的远程形式，仅界面）。
- 审计与撤销：ctl/MCP 的写入和被拒的非读调用写入日志目录的 `audit.log`（JSON 行，参数经 `applog.Redact`，2 MB 轮转一份）。写入前按方法快照：设置（撤销时保留 UI 状态与授权）、自定义规则（删新增、补回删除的、再按原顺序移动）、覆写列表、规则启停、系统代理开关、代理组选择、当前订阅、订阅编辑、导入的订阅（撤销即删除）。撤销栈在内存里，最多 20 条，只覆盖本次运行。
- 脱敏：agent 收到的每个结果都先转成 JSON 再对所有字符串做 `applog.Redact`（图标字节除外）；订阅的 `URL` 清空；设置去掉 UI 状态和授权，代理认证的密码打码。
- MCP：工具名是方法名的 snake_case；提供读、敏感读、写三级，去掉整体写设置、图标、目录路径、aTrust 文件导入导出和诊断包导出。参数的 JSON Schema 由 `app.API` 的反射类型生成。连接按需建立，澜流重启后自动重连；单次调用最长等 3 分钟（包含等用户授权）。
- 测试：`internal/rpc`（往返、握手、对端 uid 校验、权限与授权、事件、补丁预览与撤销、脱敏）、`internal/app/remote`（清单覆盖与生成文件新鲜度）、`apigen`（漏标即失败）、`internal/ctl`、`internal/mcp`（工具清单、一次完整的 `route_domain` 预览 → 应用 → 验证 → 撤销）、`internal/view`（授权弹窗）。
- 未做：大数据的增量参数（`since`/`limit`）；托盘里的授权入口（S2 拆出服务进程后再做）；Windows 管道与对端校验只经交叉编译和 `go vet`，未在真机运行。
