# 后台服务与按需界面

状态：S1、S2、S3 与大数据增量读取已实施（S1 见第 8 节，其余见第 9 节）。目标：界面关闭后进程真正退出、释放内存；代理、订阅更新、统计、检测和通知照常运行；`lanflow ctl`、MCP 和界面共用同一套本机接口。

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
- **大数据**：连接列表、日志可能很大，另有 `ConnectionsSince`、`ClosedConnectionsSince`、`LogsSince`、`AppLogsSince` 增量方法（第 9 节）；规则仍整包返回。

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

- ~~mygo 在没有窗口的进程里运行托盘、全局快捷键和系统通知~~：托盘与全局快捷键已确认（早先在宿主机上用模拟后端；此后 app 只在虚拟机里运行）；系统通知在打包的 app 里才可用，未在无窗口进程中单独确认。
- ~~界面进程退出后 macOS 的 Dock 行为~~：已确认（第 9 节）。
- 远程客户端下各页面的刷新延迟（读缓存 + 增量副本之后，未做系统测量）。
- Windows 命名管道在服务以普通用户运行、helper 以 SYSTEM 运行时的权限组合；Windows 上的整套拆分（见第 9 节「未验证」）。

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
- 授权：MCP 首次写入、MCP 敏感读、ctl `--reveal` 敏感读、ctl 危险操作都经界面弹窗（同时发系统通知并把窗口拉到前台）。写入的「始终允许 / 拒绝」存进 `Settings.AgentGrants`，「仅本次」只对当前连接有效；敏感读只按连接授权。等待超过 1 分钟视为拒绝（「等待界面确认超时」；S2 起由服务端计时，弹窗卡住也会按时返回）。没有界面可问时（`Prompt` 为空）一律拒绝并说明原因。
- 整体写设置：agent 读到的设置已脱敏，整体写回会把掩码存进去，所以 `SaveSettings`、`ApplyDNS`、`ApplySniffer` 对 agent 拒绝，改用新增的 `PatchSettings(patch, dryRun)`：按字段名（不区分大小写）合并 JSON 补丁，未知字段报错，`UI`、`AgentGrants`、`CorePath`、`CoreVersion`、`WebDAV`、`ConnHiddenColumns` 不可改；`dryRun` 返回脱敏后设置的行级差异（前后各 2 行上下文）。`EditSubscription` 收到空链接时保留原链接。
- 其他 agent 方法：`Undo`、`AuditLog(n)`、`Methods`（清单，供兼容检查）、`PatchUIState`（`UpdateUIState` 的远程形式，仅界面）。
- 审计与撤销：ctl/MCP 的写入和被拒的非读调用写入日志目录的 `audit.log`（JSON 行，参数经 `applog.Redact`，2 MB 轮转一份）。写入前按方法快照：设置（撤销时保留 UI 状态与授权）、自定义规则（删新增、补回删除的、再按原顺序移动）、覆写列表、规则启停、系统代理开关、代理组选择、当前订阅、订阅编辑、导入的订阅（撤销即删除）。撤销栈在内存里，最多 20 条，只覆盖本次运行。
- 脱敏：agent 收到的每个结果都先转成 JSON 再对所有字符串做 `applog.Redact`（图标字节除外）；订阅的 `URL` 清空；设置去掉 UI 状态和授权，代理认证的密码打码。
- MCP：工具名是方法名的 snake_case；提供读、敏感读、写三级，去掉整体写设置、图标、目录路径、aTrust 文件导入导出和诊断包导出。参数的 JSON Schema 由 `app.API` 的反射类型生成。连接按需建立，澜流重启后自动重连；单次调用最长等 3 分钟（包含等用户授权）。
- 测试：`internal/rpc`（往返、握手、对端 uid 校验、权限与授权、事件、补丁预览与撤销、脱敏）、`internal/app/remote`（清单覆盖与生成文件新鲜度）、`apigen`（漏标即失败）、`internal/ctl`、`internal/mcp`（工具清单、一次完整的 `route_domain` 预览 → 应用 → 验证 → 撤销）、`internal/view`（授权弹窗）。
- 未做：大数据的增量参数（`since`/`limit`）；托盘里的授权入口（S2 拆出服务进程后再做）；Windows 管道与对端校验只经交叉编译和 `go vet`，未在真机运行。

## 9. S2、S3 与增量读取实施说明

### 进程与入口

| 启动方式 | 模式 | 代码 |
| --- | --- | --- |
| `Lanflow --service [--login] [--open=…]` | 服务进程 | `main_split.go` `runService` |
| `Lanflow [--open=…] [lanflow://…]`（默认） | 界面进程 | `main_split.go` `runWindow` |
| `LANFLOW_SINGLE=1`，或 `<DataDir>/single-process` 文件存在 | 单进程（拆分前的行为，排障退路；`LANFLOW_SINGLE=0` 优先于文件） | `main.go` `runSingle` |
| `lanflow ctl …`、`lanflow mcp` | 客户端子命令（不变） | `main_cli.go` |

- **服务进程**：取单实例锁（拿不到说明已有服务，参数转交后退出），`ActivationPolicyAccessory`（macOS 不显示 Dock 图标，`lsappinfo` 中为 `UIElement`），启动 `app.Service`（`LANFLOW_FAKE=1` 时为模拟后端）、本机接口、托盘、全局快捷键、系统通知、唤醒与网络事件。托盘与快捷键的代码与单进程共用：`internal/view/host.go` 定义 `host` 接口，`Shell`（单进程）和新的 `view.Background`（服务进程，无窗口）各自实现；全局快捷键的注册表抽成 `keys.go`。托盘的切换节点、开关代理、切换模式、订阅、测速直接调服务；「打开澜流」「反馈问题…」以及没有 helper 时开 TUN 会拉起或唤起界面（`--open=feedback|tun|logs|<链接>`）。
- **界面进程**：`split.Connect` 连服务；`rpc.ErrNotRunning` 时用同一可执行文件带 `--service` 拉起服务（Unix 新会话 `setsid`，Windows `DETACHED_PROCESS`），最多等 20 秒；服务实在起不来时退回单进程，而不是什么都不显示。界面以 `remote.Client` 为 `app.API`，`view.Options.Remote` 只关掉三件事：托盘、全局快捷键（设置页经 `SetShortcut` 让服务注册）和登录项，以及 `System` 提醒的系统通知（由服务发）。
- **单实例**：服务持有 mygo 的单实例锁。界面不取锁，靠 `AttachUI`：服务记住当前界面的连接；第二个界面进程的 `AttachUI(arg)` 让已有界面显示并打开 `arg`，自己退出。界面关窗前先 `DetachUI`，避免新开的界面被转发给正在退出的旧界面。
- **macOS 的 Dock / Finder**：服务进程是同一 bundle 的 `NSApplication`，LaunchServices 视其为「正在运行的 Lanflow」，所以在 Finder、Dock、Spotlight 打开 Lanflow 时不会启动新进程，而是激活服务进程；服务在 `OnActivate` 中拉起界面（`OnOpenURL` 同理，深链接转交界面）。界面进程是普通前台应用，有 Dock 图标。
- **关窗**：`CloseToTray`（默认开）时关窗 = 界面进程退出，服务、内核、连接不受影响；关掉该设置时关窗 = 退出澜流（与单进程一致）。窗口快捷键在窗口位于前台时关掉窗口（即退出界面进程），否则拉起或显示。
- **退出澜流**：界面里的 ⌘Q / 应用菜单退出、以及托盘「退出澜流」都走原有确认（系统代理开着时 `Shell.AskQuit`），确认后界面调 `QuitService`，服务按原逻辑恢复系统代理、停内核，并向界面发 `ui quit` 让它直接退出。托盘退出时有界面就让界面确认（`ui ask-quit`），没有界面则用原生对话框确认（`Background.ConfirmQuit`，文字与窗口内相同）。SIGTERM 不确认，直接走同一退出路径。
- **agent 授权**：服务进程的 `Prompt` 改为 `Background.AskAgentConsent`：发系统通知，并在托盘菜单顶部加「待授权：<agent>…」，点通知或菜单项弹原生对话框（写入：始终允许 / 仅本次 / 拒绝；其他：允许 / 拒绝；关闭对话框 = 拒绝）。超时与撤回逻辑不变。单进程模式仍用窗口内的弹窗。
- **提醒（Notice）**：`TakeNotices` 只能被取走一次，而服务进程需要 `System` 提醒发系统通知、界面需要其余提醒做 toast/对话框。`split.Notices` 包住服务：每次 `TopicNotice` 由服务进程取出，新提醒交给 `Background.SystemNotice`，同时排队留给界面的 `TakeNotices`（最多 20 条，超过 2 分钟的 toast 丢弃）。

### 本机接口新增

- 窗口操作（`internal/rpc/windowops.go`，级别 `ui`，agent 一律拒绝，不进 MCP 工具表）：`AttachUI(arg) → {forwarded, pid, split}`、`DetachUI`、`QuitService`、`SetBackground(hidden)`、`SetShortcut(action, acc)`、`CacheUsage`、`CleanCaches`（设置 › 数据的可选接口）。服务端的 `rpc.Host` 由服务进程（`serviceHost`）或单进程（`singleHost`，`split=false`：第二个界面进程直接让单进程窗口显示）实现。
- 服务 → 界面的通知 `{"method":"ui","params":{"cmd":"show|toggle|ask-quit|quit","arg":…}}`。
- `hello` 的回答增加 `pid`。没有界面连接时服务处于后台节奏（`SetBackground(true)`），界面附着后恢复。

### 远程客户端（界面用）

- **读缓存**（`remote/cache.go`）：读方法（无 error 返回）的结果按方法 + 参数缓存，收到相关主题的 `changed` 事件才失效（主题 → 方法的表在 `topicReads`，不在表里的读方法遇任何事件都失效，`ConfigDir`、`LogDir`、`LookupIP`、`ATrustReference` 不失效）；调用任何动作后整体失效，动作的效果立即可见。切片结果每次复制一份，视图原地排序不影响缓存。
- **大列表副本**（`remote/mirror.go`）：`Connections`、`ClosedConnections`、`Logs`、`AppLogs` 由副本提供，事件到来后下一次读取才用增量方法补齐（每页 500 条）。事件计数用原子变量，读取线程持锁调用时不会卡住收事件的线程。
- **重连**（`remote/window.go`）：连接断开后先通知界面（toast「与澜流后台服务的连接已断开，正在重新连接」），按 0.1/0.25/0.5/1 秒重试；服务不在（`ErrNotRunning`）就拉起；连上后重新 `AttachUI`、重新订阅、清空缓存与副本，并对所有主题各发一次事件，让每个页面重读快照。退出澜流后（`Hold`）不再重连；重启服务期间等旧进程退出再连。

### 大数据增量读取

- `app.API` 新增：`ConnectionsSince(since) ConnDelta`、`ClosedConnectionsSince(since, limit) ConnPage`、`LogsSince(since, limit) LogPage`、`AppLogsSince(since, limit) LogPage`（`internal/model/page.go`），`levels.go` 中前三个为敏感读、应用日志为读。
- `internal/seqring`：`Ring` 给每个条目编号；游标为 0、早于已丢弃的条目、早于最近一次清空、或大于当前序号（服务重启过）时返回 `Reset`，客户端整体替换；`limit` 分页时 `More` 表示还有。清空会消耗一个序号，所以清空前拿到的最新游标也会得到 `Reset`。活动连接是原地变化的列表，用 `Versions` 记录每条连接最后变化的序号和最近 600 次更新内的移除，`ConnectionsSince` 只返回新增、变化的连接和移除的 ID（`model.SameConnection` 比较全部字段，有测试保证新增字段不会漏比）。
- 服务的内核日志、已关闭连接改用 `seqring.Ring`；应用日志（`applog`）同样（延迟分配不变）；模拟后端用 `seqring.SliceSince` 和 `Versions` 实现同样的语义。
- 整包方法保留不变，ctl/MCP 照旧可用。

### 开机启动与迁移（S3）

- 「开机时启动」改为启动服务进程（`internal/autostart`）：macOS 写 `~/Library/LaunchAgents/lanflow.service.plist`（`<exe> --service --login`，`RunAtLoad`，不 `KeepAlive`，限 Aqua 会话）；Windows 写当前用户 Run 键的 `lanflow.service` 值（`"<exe>" --service --login`），并像 mygo 一样清掉任务管理器里的禁用标记。内容相同时不重写；app 移动位置后服务下次启动时改写。macOS 开发构建（不在 .app 里）不写。
- 服务启动时第一次应用设置时迁移：mygo 的旧登录项（macOS `SMAppService` 主程序登录项或 macOS 12 的旧 launch agent，Windows 的 `--mygo-opened-at-login` Run 值）若存在就移除，然后按 `OpenAtLogin` 写新的。
- 登录启动的服务（`--login`）在 `SilentStart` 关闭时顺带打开界面，与拆分前登录后显示窗口一致。迁移前的最后一次登录仍由旧登录项打开界面进程：它照常拉起服务；`SilentStart` 开着时界面直接退出。
- **首次启动**：界面发现没有服务，拉起服务再连接。**升级**：界面握手发现服务版本不同，弹窗「需要重启澜流后台服务」，确认后 `QuitService`，等旧进程退出后由重连逻辑拉起新服务（内核会短暂停止，系统代理随 `AutoSystemProxy` 恢复）。macOS 上替换 app 后从 Finder 打开会先激活旧服务，旧服务按磁盘上的路径拉起的已经是新版本界面，于是同样走这个提示。**旧的单进程版本仍在运行**：它的本机接口没有 `AttachUI`，界面提示先退出旧版本（`split.ErrOldService`）。
- **崩溃恢复**：服务崩溃后界面断线，重连时发现服务不在就重新拉起；新服务接管仍在运行的内核（见「服务崩溃后接管内核」）。没有界面时服务崩溃不会自动恢复（launchd 不 `KeepAlive`，与「退出澜流后保持退出」冲突），下次打开界面时拉起。

### 与设计的差异

- 设计中「托盘弹通知请用户允许」实现为通知 + 托盘菜单项 + 原生对话框；单进程模式保留窗口内的弹窗。
- 版本不一致只在界面启动（握手）时检查一次，不做跨版本兼容。协议版本（`rpc.ProtocolVersion`）不一致时握手失败，错误里带服务的 PID（本版本起），界面结束旧服务（Unix SIGTERM，Windows `TerminateProcess`）后拉起自己的服务。
- 服务在运行却连不上时，界面带 `--fallback` 尝试单进程；拿不到单实例锁（服务还占着）就提示用户结束 Lanflow，服务也不会因此再拉起界面，避免循环。
- 规则（`Rules`）仍整包传输：它只在 `TopicRules` 时变化，读缓存已避免每帧传输。
- `UpdateUIState` 仍按设计在客户端执行函数后 `PatchUIState` 整体提交。

### 实测（虚拟机，真实后端，0.1.1 拆分版 vs 0.1.0 单进程）

| 情形 | phys_footprint |
| --- | --- |
| 拆分：窗口开着 | 界面 139 MB + 服务 31 MB |
| 拆分：关窗后只剩服务（1 分钟、3 分钟后） | 32 MB |
| 旧版单进程 | 168–170 MB |

关窗后常驻占用约 32 MB，是单进程的五分之一左右。

### 服务崩溃后接管内核

虚拟机实测发现：服务被 `kill -9` 后，界面拉起的新服务把这当成普通的异常退出（弹恢复提示、关掉系统代理和 TUN 的状态、把系统 DNS 还原），而 helper 里的旧内核仍在运行、系统代理仍指向它，于是端口被占、TUN 被当成「其他代理软件的 TUN」。现在（`internal/app/takeover.go`、`internal/core/adopt.go`）：

- 新服务创建时先找上一次留下的内核：问 helper 是否在运行 Mihomo（helper 只为安装它的用户、数据目录启动内核），再看 PID 文件指向的子进程孤儿。找到时不做恢复提示，也不还原系统 DNS。
- 启动流程（`bootstrap`）里接管它：`core.Manager.Adopt` 经 helper 的 runner（只开始轮询状态）或 exec runner（按 PID 轮询存活，结束时照常报告崩溃，停止时先发中断再强杀）接管，并要求它在本数据目录的控制接口上应答。接管后按启动成功的状态继续：TUN、系统代理（按标记重新认领，PAC 模式重启 PAC 服务）、DNS 都不变，流、检测照常开始。
- 接管不了（控制接口不应答等）：经 helper 停掉旧内核或用 PID 文件结束孤儿，再按崩溃前的设置启动，系统代理按标记认领，同样不弹提示；若这次启动也失败，才退回原来的恢复提示。
- 启动内核时如果混合端口被占，而占用者是上一次留下的内核，先停掉它再启动；判断「其他代理软件的 TUN」时排除 helper 正在运行的澜流内核。

### 事件处理里的 RPC 死锁（重连后画面停住、首次启动卡住、关窗退出慢）

虚拟机里看到的三个现象有同一个根因：本机接口客户端在读取回复的 goroutine 上直接调用事件处理函数，而界面收到 `TopicNotice` 时会在处理函数里调 `TakeNotices`。这个调用的回复要由同一个 goroutine 读，于是互相等待：之后的事件和回复全部卡住，界面停在旧画面（服务换代后尤其明显，新服务一启动就有提醒），主线程上的读调用也一直等下去，SIGTERM 时退出流程里的 RPC 同样卡住。现在 `rpc.Conn` 把事件和窗口命令按顺序交给单独的 goroutine 处理（处理函数里可以调用服务），并有测试覆盖。

另外：
- 恢复提示（「上次澜流未正常退出」）在 `Status().PendingRecovery` 变为 false 时自动关闭（经 ctl 处理、或新服务接管了内核）；重连到新的服务进程时，丢掉旧服务提出的恢复提示、配置错误对话框和未处理的提醒，再按新服务的快照重新判断。
- 关窗、⌘Q、SIGTERM 时界面立刻（不等退出流程走完）`DetachUI`；保存界面状态最多等 2 秒（`PatchUIState` 2 秒超时）。正在退出的界面若收到「显示窗口」（托盘、Finder 激活或第二个界面进程转来的），先脱离再拉起一个新的界面进程，不会被吞掉。
- 需要用户确认的 ctl/MCP 调用由服务端计时，1 分钟后返回「等待界面确认超时」，弹窗或托盘菜单卡住也不影响；托盘菜单的刷新改到后台。

### 首次启动卡住的防护

虚拟机里出现过一次（未复现）：覆盖安装后首次冷启动，界面主线程卡在 Go 的等待里，窗口不出现，SIGTERM 也不退出，同时发生了旧登录项迁移、系统「后台活动」通知和本地网络权限弹窗。排查后改为：

- 界面的读调用（每帧在主线程上）一律 3 秒超时（`remote.Options.ReadTimeout`），服务不应答时显示为空数据而不是卡死；`DetachUI` 最多等 1 秒，关窗时放到后台；`SetBackground` 由后台 goroutine 发送。
- 服务的登录项迁移与写入（SMAppService 注销、写 LaunchAgent）不再在 `WhenReady` 和设置变更的监听里同步执行，改由单独的 goroutine 排队完成（`queueAutostart`）。
- 两个进程收到 SIGTERM 后先走正常退出，5 秒内没退完就自己退出：服务先恢复系统代理、停内核、通知界面，界面先 `DetachUI`。

### 未验证

- **Windows 全部**：只经交叉编译与 `GOOS=windows go vet`。未在真机验证：分离进程启动（`DETACHED_PROCESS`）、服务拉起的界面能否抢到前台（可能需要 `AllowSetForegroundWindow`）、Run 键启动、命名管道下的重连、`Terminate` 用 `TerminateProcess` 结束无响应的旧服务（不会恢复系统代理，靠下次启动的恢复提示）。
- 内核接管只有单元测试（helper 与内核的 fake、以测试程序冒充的 mihomo 孤儿）；需要在虚拟机里复测「开 TUN + 系统代理时 kill -9 服务」。
- 首次启动卡住没能复现，上面的防护针对可能的阻塞点，未在虚拟机里验证。
- macOS 登录项：launch agent 的写入只有单元测试（临时 HOME），未在本机写入真实的 LaunchAgents，也未验证 macOS 13+「允许在后台」提示与登录时的实际启动；旧 `SMAppService` 登录项的移除未实测。
- 无窗口服务进程的系统通知（需要打包且授权通知的 app）与原生对话框（退出确认、agent 授权）未在本机点按验证。拆分模式下界面的 ⌘W、⌘Q、托盘菜单未点按（桌面自动化工具按 bundle ID 只找到服务进程），用 SIGTERM、本机接口和集成测试验证了等价路径；单进程下用菜单关过窗口（用于内存对比）。
- 协议版本不一致时按 PID 替换旧服务只有单元测试；本版本之前的服务不在握手错误里带 PID，遇到时界面提示用户结束旧进程。
