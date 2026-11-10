# 配置同步：lanflow.yaml

澜流把用户配置收拢成一个带版本号的声明式文件 `lanflow.yaml`，放进一个会被同步的文件夹（iCloud 云盘、OneDrive、坚果云、Dropbox、Syncthing……），多台设备读写同一个文件。订阅链接等密钥不进文件，只写成 `keychain:<id>` 引用，值在各台设备的系统钥匙串里。缓存、统计、窗口位置、`runtime.yaml`、界面状态等运行时数据一直留在本机。

代码：格式在 `internal/syncfile`，与服务的衔接在 `internal/app/configdoc.go`（文档 ↔ 状态）、`configapply.go`（加载）、`configsync.go`（目录、轮询、写入、冲突）、`configsecrets.go`（钥匙串）；界面在 `internal/view/settings/configsync.go`。

## 开启

「设置 › 常规 › 配置同步 › 同步位置」：

| 选项 | 文件位置 |
| --- | --- |
| 关闭（默认） | 不写文件；配置只在本机 |
| iCloud 云盘 | `~/Library/Mobile Documents/com~apple~CloudDocs/Lanflow/lanflow.yaml`；仅 macOS，且 iCloud 云盘文件夹存在时出现 |
| 自定义文件夹… | 所选文件夹下的 `lanflow.yaml`；Windows 上选 OneDrive 等同步盘里的文件夹 |

开启时文件夹里还没有 `lanflow.yaml`：写入本机配置。已有文件且与本机配置相同：直接开启。已有**不同**的文件（通常来自另一台设备）：弹窗二选一，不会悄悄覆盖——

- **使用文件中的配置**：按下文「加载」的流程校验并加载，失败则不开启；
- **用本机配置覆盖文件**：原文件先复制到 `<数据目录>/sync-backups/`，再写入本机配置。

关闭同步只停止读写，文件留在原处。同步方式本身（`Settings.Sync`）是本机项，不写进文件。

`ctl` / MCP 不能开关同步（`SetConfigSync` 为危险级），只能查看状态和加载文件。

## 文件格式

```yaml
# Lanflow configuration (docs/config-sync.md), schema: docs/schema/lanflow.schema.json
format: 1
settings:            # model.Settings 去掉本机项；键名为字段名的小驼峰（mixedPort、dns.enhancedMode…）
  mixedPort: 7890
  mode: rule
  dns:
    takeOver: true
    nameserver: [https://doh.pub/dns-query]
  ...
overrides:           # 覆写列表，按应用顺序
  - id: global
    name: 全局覆写
    enabled: true
    global: true
    text: |
      log-level: warning
  - id: 3fa2c1d0
    name: 远程覆写
    enabled: true
    global: false
    url: https://example.com/override.yaml   # 从链接下载的覆写；text 是上次下载的内容
    text: |
      ...
customRules:         # 自定义规则，按匹配顺序，位于订阅规则之前
  - type: DOMAIN-SUFFIX
    payload: openai.com
    target: 节点选择
subscriptions:       # 带链接的订阅
  - id: a1b2c3d4     # 各设备相同，覆写选择和 Wi-Fi 规则靠它对应
    name: 机场
    url: keychain:sub-a1b2c3d4
    autoUpdateHours: 12
    overrides: [3fa2c1d0]
webdavPassword: keychain:webdav-password   # 设置了 WebDAV 密码时才有
```

- `format` 是文件格式版本。本版本写 1、读 ≤1；读到更高的版本整份拒绝，不会只读一半。
- 澜流写出的文件是**规范形式**：同样的配置总得到同样的字节，所有键都写出（缺少的键在读取时取默认值，便于手写或 agent 只写一部分）。加载后会按规范形式重写，手写的注释不保留。
- 读取时：未知的键记录为警告并忽略（较新版本的设备可能写入本版本不认识的设置）；本机项的键直接忽略；类型错误、重复的键、无效 ID、订阅引用了不存在的覆写等都会拒绝，并给出行号与路径（如「第 12 行（settings.mixedPort）：应为整数」）。
- JSON Schema 由 Go 结构生成，在 [`docs/schema/lanflow.schema.json`](schema/lanflow.schema.json)。`internal/syncfile` 的测试保证它与 `syncfile.Doc` 一致：改动 `model.Settings` 后运行

  ```sh
  UPDATE_SCHEMA=1 go test ./internal/syncfile -run TestSchemaUpToDate
  ```

  编辑器（如 VS Code 的 YAML 插件）可用它补全和校验。

## 同步什么，不同步什么

同步：设置（常规、代理、DNS、嗅探、外部资源、延迟测试、快捷键、Wi-Fi 规则、下载镜像、WebDAV 地址与账号、高级）、覆写列表、自定义规则、带链接的订阅及其名称、User-Agent、经代理更新、自动更新间隔、超时和覆写选择。

不同步（排除清单即 `syncfile.Excluded`，测试保证其中每一项都对应真实字段）：

| 文件中的路径 | 原因 |
| --- | --- |
| `settings.ui`、`settings.connHiddenColumns` | 界面状态 |
| `settings.openAtLogin` | 开机启动是本机的登录项 |
| `settings.tun` | TUN 依赖本机安装的权限组件 |
| `settings.corePath` | 本机文件路径 |
| `settings.coreVersion` | 本机安装的内核版本 |
| `settings.geo.updatedAt`、`settings.webDAV.lastBackupAt` | 运行记录 |
| `settings.agentGrants` | 本机对 agent 的授权；放进同步文件等于让能写文件的程序给自己授权 |
| `settings.sync` | 同步方式本身 |
| `settings.advanced.tunDevice`、`tunIncludeInterface`、`tunExcludeInterface`、`interfaceName` | 本机网卡名 |

另外：从本地文件导入的订阅（内容只在本机）、当前使用的订阅（各设备可以不同，Wi-Fi 规则也会切换它）、订阅内容与流量信息、运行时数据都不在文件里。从本地文件导入的订阅自带的旧式订阅覆写也随订阅留在本机。

## 加载（外部变化）

1. **发现**：同步开启时每秒查看一次文件夹（文件大小、修改时间、冲突副本、iCloud 占位文件），变化稳定 1.5 秒后才读取（去抖，避免读到同步程序写了一半的文件）。
2. **识别自己的写入**：每次写入或处理过的内容都按 SHA-256 记下（`<数据目录>/sync-state.json`，重启后仍有效）。哈希相同的内容——本机刚写的、已加载的、已拒绝的——不会再处理。
3. **解析与规划**：解码（带行号的错误），算出本机加载后的配置：设置取文件中的值，本机项保留本机的；订阅按 ID 新增、修改、删除（正在使用的订阅和本地文件订阅不删）；覆写列表与自定义规则以文件为准。
4. **校验**：端口占用检查，并用新的设置、覆写和规则合成当前订阅的配置，交给 `mihomo -t`。任何一步失败都**不写入任何东西**，配置保持原状。
5. **应用**：写入订阅、覆写和规则，设置交给 `SaveSettings`，按改动分级处理——可热更新的走 PATCH，需要重载的重新生成配置并重载，TUN 等需要重启的重启内核；只有覆写或规则变了时重载一次。新出现的订阅在后台下载；本机没有当前订阅时选用第一个下载成功的。
6. **结果**：成功时弹出提示并记录「上次加载」；失败时「设置 › 常规 › 配置同步」和概览页显示横幅，给出原因和行号（`ConfigSyncStatus().Error/ErrorLine`），同一份内容只提示一次。修正文件后会再次加载。

本机改动（设置、订阅、覆写、规则）会在一秒内写回文件；写入前与上次写入或加载的规范文本比较，没有变化就不写。将要覆盖的文件若不是本机写出的版本（例如被拒绝的外部修改），先复制到 `sync-backups/`。

### iCloud 未下载的文件

iCloud 可能只留下占位文件 `.lanflow.yaml.icloud`。此时澜流运行 `brctl download <路径>` 请 iCloud 下载（最多每 15 秒一次），状态显示「正在等待 iCloud 下载文件…」，期间不读也不写；下载完成后按正常流程加载。开启同步时若遇到占位文件，会先请求下载并提示稍后再试。

### 冲突副本

两台设备同时修改时，同步程序会留下副本：iCloud 的 `lanflow 2.yaml`，Dropbox / Google Drive 的 `lanflow (1).yaml`、`lanflow (… conflicted copy …).yaml`，OneDrive 的 `lanflow-<电脑名>.yaml`，Syncthing 的 `lanflow.sync-conflict-….yaml`（含它们的 iCloud 占位文件）。澜流发现后：

- 不自动加载、不自动删除任何一份；
- 弹出通知，并在设置和概览页显示横幅，逐个副本提供「保留当前文件」和「保留此副本」（都需确认）；
- 保留副本：副本按「加载」的流程校验并加载，成功后写成 `lanflow.yaml`，原文件移入 `sync-backups/`；保留当前文件：副本移入 `sync-backups/`。两种情况都不删除文件。

同时修改的另一种结果是「后写的覆盖先写的」：同一时刻本机也有未写出的改动时，先加载外部变化，再按加载后的状态写出，本机那次改动会丢失。

## 密钥

- 订阅链接以 `sub-<订阅 ID>` 为键存入系统钥匙串（macOS 登录钥匙串，Windows 凭据管理器，经 MyGo 的 Secrets 模块），文件只写 `keychain:sub-<id>`。本机的 `profiles.json` 仍保留自己的一份链接（与之前一样，权限 0600）。同一次运行中已写入的值不重复写钥匙串。
- WebDAV 密码本来就在钥匙串（键 `webdav-password`）；设置了密码时文件写 `webdavPassword: keychain:webdav-password`。
- 另一台设备读到本机钥匙串没有的引用时，**不报错**：该订阅照常出现在列表里，标为「需要重新输入链接」（`Subscription.NeedsURL`），更新和自动更新跳过并提示；在「编辑」里填入链接后恢复，链接随后写入本机钥匙串。设置页横幅列出这些订阅；WebDAV 密码缺失也会提示。引用保留在文件里，不会因为本机没有值而被删掉。
- 文件里也接受明文 `https://` 链接（人或 agent 手写时）；加载后链接存入钥匙串，文件重写为引用。
- 同步开启时删除订阅会一并删除它的钥匙串条目。

### 为什么不用 iCloud 钥匙串同步（kSecAttrSynchronizable）

评估结论：当前做不到，因此条目不设置该属性，另一台 Mac 上需要重新输入链接。原因：

1. 带 `kSecAttrSynchronizable` 的条目只存在于**数据保护钥匙串**（iCloud 钥匙串），写入需要 `kSecUseDataProtectionKeychain`，而这要求应用以开发者团队身份签名并带 `keychain-access-groups`（或 `application-identifier`）权限，通常还需要描述文件。澜流目前标识为 `com.example.lanflow`，没有团队签名和这些权限，调用会得到 `errSecMissingEntitlement`（-34018）。
2. MyGo 的 Secrets 模块（`internal/darwin/secrets.go`，经 purego 调用 Security.framework）只设置 class / service / account / label / data，没有传入其他属性的接口；要支持需要绕过 MyGo 直接调用 `SecItemAdd`。
3. Windows 凭据管理器没有对应的跨设备同步。

等应用有了 Developer ID 团队签名后，可以在 `app.SecretStore` 之外为链接加一个「同步钥匙串」实现：以 `kSecAttrSynchronizable = true`、`kSecUseDataProtectionKeychain = true` 和团队的访问组写入，读取时用 `kSecAttrSynchronizableAny`。文件格式不需要改。

### 设计：用口令加密密钥写入同步文件（未实现）

供不用 iCloud 钥匙串（或在 Windows 与 Mac 之间同步）的用户：密钥加密后放进同步文件，任何一台知道口令的设备都能解开。本次只做设计，原因是还需要口令的设置、修改与在新设备上输入的界面，以及口令丢失、轮换的处理，工作量超出本次范围。

- **格式**：文件新增可选的顶层键（`format` 仍为 1，旧版本把它当未知键忽略）：

  ```yaml
  sealed:
    kdf: pbkdf2-sha256       # Go 标准库 crypto/pbkdf2，无需新依赖
    iterations: 600000
    salt: <base64, 16 字节>
    nonce: <base64, 12 字节>
    data: <base64>           # AES-256-GCM 密文；明文为 {"sub-a1b2c3d4": "https://…", "webdav-password": "…"}
  ```

  附加数据（AAD）取 `"lanflow-sealed-v1"`，防止把别处的密文挪过来。
- **写入**：开启「用口令加密密钥」并设置口令后，口令本身存本机钥匙串（`sync-passphrase`）；每次写文件时把所有被引用的密钥加密进 `sealed`，引用写法不变（`keychain:sub-<id>`）。口令派生的密钥按 salt 缓存，salt 只在改口令时更换，避免每次写文件都跑一遍 KDF。
- **读取**：解析 `keychain:<id>` 时先查本机钥匙串；没有且文件有 `sealed` 时，用本机保存的口令解密取值并写入本机钥匙串。本机没有口令时，设置页提示「输入同步口令以取回 N 个订阅链接」；输错口令（GCM 校验失败）只提示，不影响其余配置的加载。
- **改口令 / 关闭**：改口令时用新 salt 重新加密；关闭时删除 `sealed`。丢失口令只影响取回密钥，其余配置照常同步，用户可以在各设备上重新输入链接。
- **风险**：同步文件落在第三方云盘，安全性取决于口令强度；界面应要求一定长度并说明这一点。

## 本机接口与 agent

- `ConfigSyncStatus()`（读）：同步位置、文件路径、状态（`off` / `ok` / `waiting` / `error`）、上次加载 / 写入时间、拒绝原因与行号、冲突副本、缺少的钥匙串条目。`lanflow ctl config sync status`。
- `ApplyConfigFile(path, dryRun)`（写，可撤销）：按「加载」的流程加载任意一个 `lanflow.yaml`（`path` 为空表示同步文件），返回改动的部分和差异；`dryRun` 只返回差异。差异是本机 `lanflow.yaml` 的文本，订阅链接只以引用出现。`lanflow ctl config sync apply [文件] [--dry-run]`；MCP 工具 `apply_config_file`。
- `SetConfigSync`、`ResolveConfigConflict` 为危险级：MCP 不提供，ctl 每次都要在澜流里确认。
- agent 可以直接编辑同步文件夹里的 `lanflow.yaml`，澜流校验通过后自动加载，不通过则保持原状并在 `config_sync_status` 里给出行号（见 `skills/lanflow/SKILL.md`）。注意这条路径不经过 MCP 的逐次授权，能写同步文件夹的程序都能改配置；`agentGrants` 等本机授权因此不在文件里。

## 导出、导入与 WebDAV 备份

- 「导出…」写出 `lanflow.yaml` 格式（不含本机项）；勾选「包含订阅链接」时链接以明文写出（文件要拿到别的设备导入），否则不含订阅。
- 「导入…」读 `lanflow.yaml`，也读旧版本导出的 JSON；两者都按原有的合并方式导入（设置覆盖、覆写与规则追加去重、订阅按链接新增），本机项（TUN、内核路径、agent 授权等）保持不变。文件中的 `keychain:` 引用在本机钥匙串中找得到就用，找不到的订阅在导入结果中列出。
- WebDAV 备份上传 `lanflow-backup-<时间>.yaml`（含订阅链接）；`.json` 的旧备份仍会列出并可恢复。

## 手动验证（iCloud）

自动测试用临时目录模拟 iCloud 云盘、占位文件和冲突副本（`internal/app/configsync_test.go`），没有在真机或虚拟机上开启 iCloud 同步。真机验证步骤：

1. 两台登录同一 Apple ID、打开 iCloud 云盘的 Mac，A 上「同步位置」选 iCloud 云盘，确认 `~/Library/Mobile Documents/com~apple~CloudDocs/Lanflow/lanflow.yaml` 生成，订阅链接为 `keychain:` 引用。
2. B 上选 iCloud 云盘，应弹出二选一；选「使用文件中的配置」，订阅出现并标为「需要重新输入链接」，设置与 A 一致，TUN 开关不受影响。
3. A 上改代理端口：数秒内 B 的端口随之改变、内核重载；在 B 上改模式，A 跟随。
4. 用文本编辑器把某个设置改成非法值保存：设置页与概览页出现带行号的横幅，配置不变；改回后横幅消失。
5. 在 B 的访达里对 `lanflow.yaml`「移除下载」，看到「正在等待 iCloud 下载文件…」，随后自动恢复。
6. 断开 B 的网络，两边各改一项后恢复网络，等待出现 `lanflow 2.yaml`，横幅提供两个选择，所选结果生效、另一份进入 `sync-backups/`。
