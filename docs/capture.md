# HTTPS 选择性解密（设计）

状态：M0 已完成（结论见 §14），M1 起按里程碑实现中（实现状态见 §15）。取代本文件旧版「解密层放在 Mihomo 前面」的方案。本文由设计草案与一轮对抗式评审合并而成：评审指出缺陷的地方以评审的修正为准。

标注：**已验证** = 在 Mihomo v1.19.33 darwin/arm64（`bin/mihomo`）上实测，全部监听 127.0.0.1、未开 TUN；**待验证** = 写明验证方法，在 M0 完成。

## 1. 目标与非目标

目标：

- 按**域名（含通配）**和/或**程序（进程名、路径）**选择性解密 HTTPS，查看请求/响应头、正文、耗时与 TLS 信息，用于审计本机应用外发了什么数据。
- **未选中的流量路径与现在完全一致**：不经过解密层，客户端看到的是服务器原证书。
- 解密不改写流量：不加头、不改压缩、不改 h2 参数，被审计对象的行为不因审计而改变。

非目标：

- 不解密局域网其他设备的流量（即使开了 allow-lan）。
- 不改写请求或响应（没有 rewrite / 脚本 / 重放）。
- 不提供「全部解密」，不提供导入外部 CA 或导出私钥。
- 不修改 Firefox、Java、Node 等第三方信任库，只给出说明。
- 不分叉或修改 Mihomo 内核，不采用第三方分叉（如 `IamAbler/mitmhomo`，仅 HTTP/1.1，且会脱离 `scripts/fetch-core.sh` / `internal/app/coresums.go` 的固定版本校验链）。

## 2. 结论与关键约束

| 结论 | 依据 |
| --- | --- |
| Mihomo 主线没有 MitM，解密层由澜流用 Go 自写 | 官方规则文档无 MITM/rewrite；旧版本文件检查过源码 `3f27ef3` |
| 采用「Mihomo 规则分流 → http 出站 → 解密进程 → 专用 listener 回流」 | 系统代理与 TUN 统一；未选中流量零影响；内核不改。草案标为待验证的内核能力**全部已验证** |
| 放弃「解密层放在 Mihomo 前面」 | TUN 下无效；开启后全部系统代理流量都经过澜流进程 |
| 解密层是**独立子进程** `lanflow capture-agent`，从 M1 开始 | GUI 崩溃时内核仍在运行（`internal/core/pidfile.go` `killOrphan`；TUN 下由 helper 托管），进程内解密层会让选中应用断网；同时隔离 96 MiB 软上限（`main_pprof.go`）与私钥 |
| TLS 终止后**明文字节原样双向拷贝**，旁路解析一份副本 | `net/http` / `http2` 重组会补 `Accept-Encoding`、`User-Agent`，改头名大小写和顺序，改 h2 SETTINGS/HPACK 指纹 |
| 规则层只是第一道过滤，**解密进程自己执行选择集**并带回环熔断 | 订阅可引用解密出站造成任意解密与无限回环（已复现：2 秒内回环 625 层） |
| 只解密本机来源（回环 / TUN） | allow-lan 时局域网设备的流量也会命中域名规则 |

关键约束：

- 只在 `mode: rule` 生效；全局 / 直连模式下显示「暂停（仅规则模式生效）」。
- 每次开关都要 reload 并 `DELETE /connections`：**已验证** reload 不切断在途连接（4 秒请求在 `PUT /configs?force=true` 后仍返回 200），不主动断开的话已解密长连接会一直留在解密层。
- 同一用户下的代码视为完全受信，本设计不对其做承诺（见 §8）。

## 3. 架构

### 3.1 流量

```text
              ┌──────────────────── Mihomo（规则模式）─────────────────────┐
应用 ─系统代理/TUN─▶ mixed-port / tun                                       │
              │  rules：coexist(aTrust) → ①② 抓包 → 自定义 → IPv6 拒绝 → 订阅 │
              │  ① 选中 ∧ UDP 443 → REJECT（M2）                              │
              │  ② ¬回流 ∧ TCP ∧ 端口 ∧ 本机来源 ∧ ¬旁路 ∧ 选中 → LANFLOW-MITM-<r> │
              └────┬───────────────────────────────────────▲──────────────┘
                   │ CONNECT host:port（http 出站，带口令）  │ CONNECT（listener
                   ▼                                         │ lanflow-mitm-up，带口令）
              ┌──────── lanflow capture-agent（子进程）──────┴────────┐
              │ 127.0.0.1:<随机端口>，校验口令与来源                      │
              │ 读 ClientHello → 执行选择集 → 解密 / 透传 / 重放透传      │
              │ 解密：上游先握手 → 叶子证书握手 → 明文字节双向拷贝        │
              │        └─ 副本 → h1/h2/WS 旁路解析 → 记录 → IPC → GUI     │
              └───────────────────────────────────────────────────────────┘
                                         回流后按订阅规则选节点出站
```

- 回流连接以域名进入 Mihomo，由 Mihomo 做 DNS 与选节点，解密进程不做 DNS（hosts 例外见 §9）。
- 解密进程只拨回流入口，用不读环境代理的 `net.Dialer`。
- **已验证**：全局 `interface-name: en0` 下 http 出站连 127.0.0.1 仍成功（非全局单播地址跳过网卡绑定）。**待验证（VM）**：TUN `auto-detect-interface` 下同样可达——步骤见 §14.2 V1。
- **已验证（M0）**：目标已被解析过时（hosts 映射，或前面任何 IP 类规则触发了解析），http 出站发出的是 `CONNECT <IP>`，回流因此丢失域名。agent 用内核 `/connections` 中该连接的 `metadata.host` 还原域名，回流、ECH 判定、记录都用还原后的名字（§14.1）。

### 3.2 规则模板

`<r>` 为每次启动随机的 8 字节 hex。

```yaml
proxies:
  - { name: LANFLOW-MITM-<r>, type: http, server: 127.0.0.1, port: <agent 端口>,
      username: lanflow, password: <32 字节随机> }
listeners:
  - { name: lanflow-mitm-up, type: mixed, listen: 127.0.0.1, port: <随机>, udp: false,
      users: [{ username: lanflow, password: <另一随机值> }] }
rule-providers:
  lanflow-mitm-sel:    { type: inline, behavior: classical, payload: ["DOMAIN-WILDCARD,*.example.com", ...] }
  lanflow-mitm-bypass: { type: inline, behavior: classical, payload: ["DOMAIN-SUFFIX,apple.com", ...] }
rules:
  - AND,((NOT,((IN-NAME,lanflow-mitm-up))),(NETWORK,TCP),(DST-PORT,80/443/8443),(OR,((SRC-IP-CIDR,127.0.0.0/8),(SRC-IP-CIDR,::1/128),(IN-TYPE,TUN))),(NOT,((RULE-SET,lanflow-mitm-bypass))),(RULE-SET,lanflow-mitm-sel)),LANFLOW-MITM-<r>
```

已验证的语法（`mihomo -t` 确实会对错误规则报 failed）：

| 项 | 结果 |
| --- | --- |
| listener `type: mixed` + `users` + `udp: false` + `listen: 127.0.0.1` | 通过；无口令/错口令 HTTP 与 SOCKS5 均 407；全局 `skip-auth-prefixes: [127.0.0.1/8]` 不影响 listener 的 `users` |
| `allow-lan: true` + `bind-address: '*'` | mixed-port 变成 `*`，listener 仍只监听 127.0.0.1 |
| `DST-PORT,80/443/8443`、`8000-9000/443` | 通过；`DST-PORT,80,443` 失败（逗号不能分隔端口） |
| inline rule-provider（`domain` 含 `*.`/`+.`，`classical` 含 `PROCESS-NAME`） | 通过；`/providers/rules` 显示 `vehicleType: Inline` |
| `AND` 内嵌 `NOT`/`OR`/`RULE-SET`；`IN-USER`、`IN-PORT`、`SRC-IP-CIDR`+`IN-TYPE,TUN` | 通过 |
| `IN-TYPE,MIXED`、`UID,501`（macOS） | 失败：入站类型只有 HTTP/HTTPS/SOCKS5/TUN 等；macOS 不能按 UID 区分用户 |
| `PROCESS-NAME-WILDCARD`、`PROCESS-PATH-WILDCARD`、`DOMAIN-WILDCARD,ocsp.*` | 通过 |
| 运行时防环 | 选中连接命中规则 ②；回流连接只命中后续规则，未再进入解密层；未选中连接不出现在解密层日志 |

### 3.3 compose.go 组装顺序

`internal/profile/compose.go` 的各层都是前插，后调用者排在前面。新增 `internal/profile/capture.go`，`Input` 增加 `Capture`（零值不生成任何内容）：

1. 订阅 → 各覆写层 → 澜流设置（DNS / 嗅探 / IPv6 拒绝）→ `addCustomRules` → 托管字段 → 探测 listener（现状）。
2. `applyCapture(c, in.Capture)`：先 `removeNamed` 删除同名 proxy / listener / rule-provider，再写入 §3.2 各项。
3. `applyAdvanced`、`applyCoexist`（现状）。最终规则顺序：coexist → 抓包 → 自定义 → `ipv6RejectRule` → 订阅。
4. **引用清理** `scrubCaptureRefs`：扫描全部非澜流层节点（`c.nodeSrc` 判来源），删除 `rules`、`sub-rules`、`proxy-groups[].proxies`、`listeners[].proxy`、`tunnels[].proxy` 中引用 `LANFLOW-MITM*` / `lanflow-mitm-*` 的项，每删一项记一条告警。
5. 所有带 `include-all`、`include-all-proxies` 或 `filter` 的组（含 `LANFLOW-PROBE`）**追加** `exclude-filter`：Mihomo 用反引号分隔多个正则，不能覆盖原值。**已验证** `exclude-filter: "^LANFLOW-MITM$"` 能把出站从 `include-all` 组中排除。
6. `strippedKeys` 清理 → `applyAdvancedPorts` → `resolveDanglingAliases`（现状）。

配套 `internal/profile/compose_capture_test.go` 黄金测试：规则顺序；覆写中 `MATCH,LANFLOW-MITM`、包含它的 select 组、`include-all` 的 `url-test` 组、指向它的 listener 均被清除；GLOBAL 组。

GLOBAL 组无法排除（**已验证**：`all` 总含解密出站，`exclude-filter` 无效）。缓解：代理页过滤该名；每次 reload 后若 GLOBAL 选中它则重置为 DIRECT；随机名让 `profile.store-selected` 缓存自然失效；解密进程熔断兜底。

运行时配置视图（`docs/design.md` §6.10）中这些行归属新层「抓包」，口令打码。

## 4. 选择性解密

### 4.1 选择项（`model.CaptureSettings`，存于设置 JSON）

| 字段 | 说明 |
| --- | --- |
| `Enabled` | 总开关；每次启动强制为 false，订阅/覆写无法开启 |
| `Domains` | `example.com` → `DOMAIN`；`*.example.com` → `DOMAIN-WILDCARD`（不含自身）；`+.example.com` → `DOMAIN-SUFFIX` |
| `Apps`（M2） | 每项独立 `IN-USER` 口令与出站；macOS 默认生成 `PROCESS-PATH-WILDCARD,/Applications/X.app/*` |
| `Bypass` | 用户跳过列表，叠加内置清单 |
| `Ports` | 默认 `80/443/8443`（`/` 分隔） |
| `AutoOffMin` | 默认 30；0 需确认 |

程序选择要点：

- 请求多数不是主进程发的：Chrome → `Google Chrome Helper`，Electron → `X Helper`，Safari → `com.apple.WebKit.Networking`（所有 WebKit 应用共用），后台下载 → `nsurlsessiond`。**已验证** `PROCESS-PATH-WILDCARD,/usr/*` 匹配 `/usr/bin/curl`，`*` 跨越 `/`，所以 `.app/*` 能覆盖 Helper 子进程。
- 依赖 `find-process-mode`（`model.FindProcessModes`：off / strict / always）。存在程序选择项时 compose 强制 `strict`，或在 UI 阻止并提示。**已验证** `strict` 下写在 inline classical `RULE-SET` 中的 `PROCESS-NAME` 能匹配 curl。
- 系统代理模式下回环入站能查到进程：**已验证**（`/connections` 中 `process`/`processPath` 有值）。

### 4.2 内置旁路清单（可逐条禁用，不可整体删除）

| 类别 | 内容 |
| --- | --- |
| 系统更新与证书 | `+.apple.com`、`+.icloud.com`、`+.mzstatic.com`、`+.apple-cloudkit.com`、`+.push.apple.com`、`+.windowsupdate.com`、`+.update.microsoft.com`、`+.delivery.mp.microsoft.com`、`+.digicert.com`、`ocsp.*`、`crl.*` |
| 身份与密码管理 | `login.microsoftonline.com`、`+.okta.com`、`+.1password.com`、`+.bitwarden.com`、`+.lastpass.com` |
| 银行与支付 | 只内置一小批（如 `+.alipay.com`、`+.95516.com`、`+.paypal.com`、`+.stripe.com`），其余由用户补充 |
| 内网 | RFC1918、CGNAT `100.64.0.0/10`、`.local`、`.lan`、`.corp`、单标签名（以 `IP-CIDR` 类条目写入 classical 清单；单标签名由解密进程判定） |
| aTrust 兼容层 | `Coexist.CorpDomains` / 进程 / CIDR 的规则排在最前，天然不解密 |

澜流自身的订阅更新、内核下载不经 Mihomo，不受影响。

### 4.3 来源限制

- 规则 ② 带 `(OR,((SRC-IP-CIDR,127.0.0.0/8),(SRC-IP-CIDR,::1/128),(IN-TYPE,TUN)))`，即使开了 allow-lan 也只解密本机流量；解密进程在 accept 时再按 Mihomo 连接元数据校验一次来源。
- **待验证**：本机开了网关/共享时 TUN 会收到非本机来源，需要再排除——在 VM 中开 TUN + 共享复现后确定规则。

## 5. 解密层

### 5.1 进程模型

| 项 | 做法 |
| --- | --- |
| 形态 | 同一二进制的子命令 `lanflow capture-agent`，由 GUI 拉起；代码在 `internal/capture/{agent,mitm,ca,trust,store}` |
| IPC | 本地 IPC 传递记录增量（`Since(seq)`）与控制命令，带随机 token；GUI 侧 `internal/app/capture.go` 新增 `TopicCapture`，复用 `emitter.go` 节流 |
| 内存 | 独立 `GOMEMLIMIT`；拷贝缓冲用 `sync.Pool`；并发解密连接上限 256，超出透传并标记（每连接约 100–150 KB） |
| 私钥 | 只在 agent 进程内使用，GUI 进程不加载 |
| 崩溃 | agent 退出时 GUI 立即 reload 去掉规则并 `DELETE /connections`；澜流启动时发现残留抓包配置先 reload 去掉 |
| 入口加固 | 常量时间比较口令；CONNECT 头限长与超时；拒绝目标为 agent 端口或回流端口本身的 CONNECT（不能一概拒绝回环：hosts 可合法映射到 127.0.0.1） |

### 5.2 连接处理

1. accept → 校验口令与来源 → 回 200 → `Peek` 首包。明文 HTTP 同样以 CONNECT 到达（**已验证**，无需处理绝对 URI 形式）。非 TLS、非 HTTP 字节 → 透传，记「非 HTTP」。
2. 解析 ClientHello（SNI、ALPN、ECH 扩展 0xfe0d），**缓存原始字节**。
3. 决策（按序）：总开关关 → 透传；**CONNECT 主机 / SNI 不在选择集**（M2 起：或口令不对应某个程序选择项）→ 透传；命中旁路或临时跳过表 → 透传；ECH 判定成立 → 透传；回环熔断触发 → 拒绝并告警；否则解密。
4. **回环熔断**：回环时 agent 收到的首包正是自己刚写给上游的首包。agent 记录每条上游连接写出的首包前 64 字节（10 秒内）：TLS 首包含 32 字节随机数，命中一次即判回环；明文 HTTP 可能合法重复，同一前缀命中两次才判。另设速率兜底：同一目标 N=1000 毫秒内 accept 超过 K=100 次即拒绝。M0 实测：不熔断时一次请求 2 秒内嵌套 5826 层（约 2900 层/秒）；熔断后第 3 个 accept 即拒绝（原连接、回流的上游握手、透传重放各一次）。
5. 上游先握手：经回流入口拨号，用客户端 SNI 与 ALPN 列表握手；成功后用叶子证书与客户端握手，`NextProtos` = 上游结果。
6. 两侧 ALPN 一致后**原样双向拷贝明文**；副本送解析器，解析失败只影响记录不影响转发。

### 5.3 协议

| 协议 | 处理 |
| --- | --- |
| HTTP/1.1 | 增量解析器；管线化、`Connection: close`、100-continue 由字节透传天然正确 |
| HTTP/2 / gRPC / SSE | 帧解析器，每方向一个 HPACK 解码器（与直连同一字节流，动态表一致）；trailers、长流、RFC 8441 天然支持；正文 tee 进截断缓冲，不整体缓存 |
| WebSocket | M1 记握手与字节数；M3 解析帧（文本帧前 N KB） |
| 未知 ALPN（`grpc-exp`、`apns-security-v3`…） | 照常拷贝，记「未知协议」，只记字节数 |
| HTTP/3 | 系统代理下 UDP 不经代理，**不可见**，UI 明示并建议 TUN；TUN 下 M2 加规则 ① 对选中目标拒绝 UDP 443 迫使回落 TCP。按域名拒绝需要 fake-ip 或 QUIC 嗅探（`compose.go` `snifferNode` 已支持 QUIC 端口）。**待验证**：REJECT 对 UDP 是丢包还是快速失败、Chrome 回落耗时，必要时比较 `REJECT-DROP` |

### 5.4 TLS 边界情况

| 情况 | 处理 |
| --- | --- |
| ECH | 判定条件：外层 SNI ≠ 目标主机，且不是其后缀匹配；CONNECT 主机为 IP 时只看 SNI 本身。**不能**「有 0xfe0d 扩展即透传」：Chrome 117 起即使目标无 ECH 配置也发 GREASE ECH，会把 Chrome 全部透传。**待验证**：为 public_name 签证书触发客户端安全降级（关闭 ECH 重试）是否可得到可解密连接——在 Chrome、Firefox 上实测 |
| 证书固定 | 只把下游握手收到的明确 alert 计为固定：`bad_certificate(42)`、`certificate_unknown(46)`、`unknown_ca(48)`、`access_denied(49)`（Go 返回 `remote error: tls: …`）。同一 (选择项, SNI) 5 分钟内 2 次 → 临时跳过表，记录列表显示「证书固定」并给「永久跳过」。「握手后零数据关闭」只计数不触发（Chrome 预连接、Safari 连接竞速很常见）。前两次失败期间应用确实会出错，首次说明页写明 |
| 上游证书无效 | 不中止：丢弃上游连接，**重新拨号并原样重放缓存的 ClientHello，整体透传**。客户端看到真实证书和真实错误，可照常「继续访问」；记「上游证书无效，已透传」 |
| 上游要求客户端证书 | 上游出现 `CertificateRequest` → 同样重放透传（本次连接即生效）；M3 可为指定主机导入 PKCS#12 |
| 上游校验 | 系统根验证后，用 `VerifyPeerCertificate` 检查链末端指纹不是澜流 CA（macOS 平台验证器无法从根集合排除单个根） |
| 上游指纹 | 上游 ClientHello 是 Go 指纹，可能遇到 bot 管理的挑战页 / 403；UI 失败原因单列「上游 403 / 挑战页」。`refraction-networking/utls` 复刻指纹为可选项，M1 不做 |
| 无 SNI / 只有 IP | CA 排除全部 IP（§6），只能透传 |
| 0-RTT | 下游不启用，客户端走完整握手 |

## 6. 根证书生命周期

| 项 | 做法 |
| --- | --- |
| 生成 | 首次启用时生成 ECDSA P-256，`CN=Lanflow Local CA (<主机名> <日期>)`，`O=Lanflow`，`CA:true, pathlen:0`，`certSign|cRLSign`，128 位随机序列号，**有效期 90 天** |
| 名称约束 | `excludedSubtrees`：高价值域（银行、Apple ID、Microsoft 登录等）+ 全部 IP（`0.0.0.0/0`、`::/0`）。名单写进 CA 后只能靠重签更新，这是接受的取舍。**已验证（M0）**：Go `x509`、macOS SecTrust（`security verify-cert -r` 临时锚点，不改钥匙串）、系统 curl（LibreSSL `--cacert`）三者都拒绝违反约束的叶子（子域、顶域、IP SAN 都拒）。**待验证（VM）**：根装进登录钥匙串并设为信任后，Safari / Chrome 是否同样执行（§14.2 V3） |
| 私钥：Windows | **M1 起**用 CNG Software KSP 不可导出密钥（`NCRYPT_EXPORT_POLICY = 0`），不放凭据管理器（同用户进程可 `CredRead`，无弹窗） |
| 私钥：macOS | M1 用 `main_secrets.go` 的 MyGo Secrets（登录钥匙串）。未签名应用每次升级会弹「允许访问」，见 §11；Secure Enclave 降为研究项（需 cgo+Swift 桥接或钥匙串访问组权限，未签名应用拿不到） |
| 无凭据库 | 拒绝启用解密，不落盘明文私钥（`internal/app/backup.go` 的混淆文件回退不可用于此） |
| 叶子 | SAN = SNI，精确名不签通配；`now-1h ~ now+7d`；会话内单独 P-256 叶子密钥；LRU 1024 张，按主机 singleflight |
| 信任安装 | macOS `security add-trusted-cert -r trustRoot -p ssl -k ~/Library/Keychains/login.keychain-db`（用户域，只在 GUI 进程调用；helper 不新增证书命令）；Windows `CertAddEncodedCertificateToStore(CurrentUser\Root)`（系统强制确认框） |
| 信任状态 | 用 CA 现签 `trust-check.lanflow.invalid` 叶子，按系统根 `Verify` 的结果为准，**不看命令退出码**（MDM 拒绝、GPO 关闭用户根时调用可能「成功」但不生效）；同时扫描 `O=Lanflow` 的旧证书提示清理 |
| 自动移除 | **关闭解密 7 天后自动移除信任并销毁私钥**；下次开启重新生成（多一次授权弹窗） |
| 重新生成 | 新 CA → 安装信任 → 移除旧信任与私钥 → 清空叶子缓存 → 断开解密连接 |
| 卸载 | macOS 设置页「移除根证书并删除私钥」；Windows 卸载程序调用 `lanflow ca-remove`，但提权安装程序的 CurrentUser 可能是管理员，故提示用户先在应用内移除，或下次登录时清理 |
| 排除 | 私钥不写日志、不进 WebDAV 备份（`backup.go`）、不进诊断包；「导出 CA」只导出证书 PEM |

不可导出只防带走、不防滥用：本机恶意代码仍可让 CNG/钥匙串替它签名（签名预言机），文档写明。

第三方信任库（只展示命令与导出 PEM，不自动修改）：

| 运行时 | 说明 |
| --- | --- |
| Electron 应用 | 渲染进程走 Chromium 用系统根；**主进程 Node `https` 用内置 CA 列表，不信任系统根**。VS Code 扩展宿主、Slack、Discord、Cursor 的 API 与自动更新多在主进程，会表现为固定/失败。选择 Electron 应用时提示需 `NODE_EXTRA_CA_CERTS`（多数应用无法注入） |
| Node | `NODE_EXTRA_CA_CERTS`；`--use-system-ca`（v23.8 加入，回溯到 22.15）；`NODE_USE_SYSTEM_CA=1`：**已验证** Node 26.10 生效（默认 121 张内置根 → 136 张，含系统根 15 张），最早版本未查 |
| Firefox | 120 起默认导入用户添加的根（`security.enterprise_roots.enabled`），可被用户或策略关闭；ESR 115 不导入。只读检测版本与 prefs |
| Python / Java / Rust | certifi / 自带 cacerts / rustls+webpki-roots 不可注入；给出 `SSL_CERT_FILE`、`keytool` 等说明 |
| curl | Homebrew curl 用 `CURL_CA_BUNDLE`。**已验证**：macOS 26 系统 curl 8.7.1 默认走 LibreSSL + `/etc/ssl/cert.pem`，**不读钥匙串**；`CURL_SSL_BACKEND=secure-transport` 时才用钥匙串信任 |
| 系统服务 | macOS 用户域信任对 root 守护进程无效；Windows LocalSystem 服务不读 CurrentUser\Root，走 WinHTTP 的程序不用系统代理 |

## 7. 捕获存储与界面

### 7.1 存储（agent 进程内）

| 项 | M1 | 之后 |
| --- | --- | --- |
| 记录环形缓冲 | 5000 条（每条约 2–4 KB） | 可调 1000–50000 |
| 单个正文上限 | 请求/响应各 1 MiB，超出截断并标「原大小 N」 | 64 KiB–16 MiB |
| 溢写 | **已实现（提前到 M1）**：正文内存上限 64 MiB，超出后把最早完成记录的正文封存到 `<DataDir>/capture/spill/session-<id>.bin`，0600，AES-256-GCM 密钥只在 agent 内存，上限 512 MiB（满了丢弃正文并标记），退出即删；崩溃残留下次启动清理 | — |
| 持久化 | 无，退出即清空 | M3：加密会话文件（口令派生密钥） |

`Flow{ID, Seq, Start, Timing, ConnID, UpstreamConnID, Process, ProcessPath, Attribution(certain|ambiguous|unknown), Selector, Method, Host, Path, Status, ReqHeader, RespHeader, ReqBody, RespBody, TLS, Proto, Error, Flags(pinned|ech|passthrough|ws|truncated)}`。透传连接也生成一条连接级记录，便于看到「哪些没能解密」。

### 7.2 界面

- **入口**：连接页行菜单（`internal/view/conns/page.go` `rowMenu`，在「此域名始终走…」之后）加「解密此主机…」（M1）、「解密此程序…」（M2，`cn.Process == ""` 时禁用）。弹窗显示将生成的选择项、是否同时开启总开关；CA 未信任时先引导安装。
- **默认看得到所有请求（参考 Surge「最近请求」）**：抓包页无论解密开关如何都列出内核看到的全部连接（主机、程序、规则、链路、流量，标「未解密」），不需要根证书；在行菜单或详情里点「解密此主机 / 解密此程序」加入选择（首次使用先看说明），之后该主机/程序的新请求以完整记录出现。解密两段腿（到 `LANFLOW-MITM-*` 的连接、`lanflow-mitm-up` 回流）由 `Connection.CaptureLeg` 标记并隐藏，由解密记录代替。「只看已解密」只显示记录。明文 HTTP 不额外解析（不改变未选中流量的路径）。
- **抓包页** `internal/view/capture`：列表 + 右侧详情，交互同连接页（`list.CellMenu` / `HeaderMenu`）。列：时间、程序（`procicon`）、方法、状态、主机、路径、大小、耗时、标记。
- **详情页签**：概要（URL、状态、归属与置信度、选择项、回流节点链路、TLS 版本/套件/ALPN/上游证书链指纹、ECH）｜请求头｜请求体｜响应头｜响应体｜时间线（accept、ClientHello、上游 dial、上游 TLS、下游 TLS、TTFB、结束）｜WebSocket（M3）。
- **正文查看器**：M1 文本与 JSON；M2 表单、multipart、图片、十六进制。按 `Content-Encoding` 解压副本展示，超过 256 KiB 先渲染前段。
- **筛选**：程序、主机（通配）、状态段、方法、内容类型、仅解密 / 含透传、URL+头全文搜索；正文搜索为显式操作，后台执行。
- **指示**：状态条「解密中 · N 个主机 · M 个程序 · 剩余 mm:ss」，可「延长 30 分钟」；托盘图标角标与「HTTPS 解密：开/关」；窗口标题加「· 解密中」；非规则模式显示「暂停」。睡眠唤醒后已超时立即关闭。首次开启须阅读说明页并勾选确认。

### 7.3 脱敏

- 显示默认打码：`Authorization`、`Proxy-Authorization`、`Cookie`、`Set-Cookie`、`X-Api-Key`、`X-Auth-Token`、名称含 `token|secret|session|password|signature` 的头；同名查询参数与 JSON 键（`access_token`、`refresh_token`、`id_token`、`password`、`client_secret`）；form 与 multipart 字段。格式 `abcd…(32 字节)`，可逐项点按显示原值（不持久）。
- 打码覆盖不到：GraphQL 变量、URL 路径中的 token（`/reset/<token>`）、`Location` 头、WebSocket 帧、任意正文。因此正文类导出受限（下节）。

### 7.4 导出限制

| 导出 | 阶段 | 规则 |
| --- | --- | --- |
| HAR、复制为 cURL | **M1 不做** | M3 加入：HAR 默认不含正文，正文为单独选项；`_lanflow` 扩展放进程、选择项、链路 |
| 复制 URL / 头 / 正文、保存正文到文件 | M2 | 含未打码凭据时二次确认并提示剪贴板历史、通用剪贴板同步风险；保存文件为明文，提示 |
| Agent / ctl | — | 见 §10，永不提供 cURL |

## 8. 安全与隐私

| 威胁 | 影响 | 缓解 |
| --- | --- | --- |
| 订阅/覆写引用解密出站（`MATCH,…`、组、`url-test`+`include-all`、listener、tunnels、sub-rules） | 任意乃至全部流量被解密；无限回环（已复现） | 随机出站名；`scrubCaptureRefs`；`exclude-filter` 追加；agent 执行选择集；回环熔断 |
| GLOBAL 组选中解密出站 | 全部流量进解密层并回环 | 代理页隐藏；reload 后重置；随机名使 `store-selected` 失效；熔断 |
| allow-lan 下局域网设备 | 他人流量被解密或全部证书报错 | 规则来源限定 + agent 二次校验 |
| CA 私钥被盗 | 网络中间人长期冒充任意站点（受名称约束的域除外），即使解密已关闭 | 90 天有效期；关闭 7 天后自动移除信任与私钥；Windows CNG 不可导出；`excludedSubtrees`（macOS 是否执行待验证）；只装用户域 |
| 本机其他用户访问 agent / 回流入口 | 借道解密或冒充回流 | 只监听 127.0.0.1；每次启动随机口令（运行时配置 0600、目录 0700）；常量时间比较 |
| 同一用户下的恶意代码 | 读配置、改规则（能访问 controller 即能 `PUT /configs`）、用私钥签名 | 不承诺防护，文档写明「同一用户 = 完全受信」 |
| 明文落点：诊断包 | `diag.go` 附带 `config/runtime.yaml`，`redactConfig` 不打码 inline payload（选中域名、程序路径）；`mihomo.log` info 级逐条记 `using LANFLOW-MITM` | diag 对抓包层专门打码（只留是否启用与数量）；抓包期间 log-level 限制为 warning 或 diag 过滤相关行 |
| 明文落点：pprof、swap、崩溃转储 | `goroutine?debug=2` 暴露 URL 片段，对本机任何用户可见；Windows pagefile 默认不加密；WER LocalDumps 生成完整转储 | 抓包开启时 agent 不开 pprof，GUI 的 `LANFLOW_PPROF` 被禁止或要求 token；数据与私钥在 agent 进程，缩小 GUI 转储暴露面；文档写明 |
| 剪贴板、文件导出 | 凭据外泄 | §7.4 |
| 证书固定误判 | 用户选中的域名静默停止解密 | 只认明确 alert；按 (选择项, SNI) 计 |
| 企业设备 | MDM 禁止添加信任；EDR 把「装根证书 + 本地代理」判为恶意；违反 DLP | 检测受管设备（`profiles status -type enrollment`；`dsregcmd /status` 或策略注册表）时额外警示；默认不允许按程序选择 aTrust 等 VPN 客户端 |
| Windows 杀软 | 未签名 exe 被判 HackTool/PUA，SmartScreen 拦截 | 发布前 VirusTotal 与 Defender 实测，被拦则只能签名 |
| 法律 | 解密他人通信可能违法 | 首次说明页：仅限本机本人、企业设备风险、会看到网页中他人内容、导出前检查打码 |

## 9. 与现有功能的交互

| 功能 | 交互与处理 |
| --- | --- |
| 系统代理 / TUN | 同一套规则。系统代理下 UDP/QUIC 不可见；TUN 下回环出站可达性待验证（§3.1） |
| 订阅中的 `PROCESS-*` 规则 | 回流连接在 Mihomo 看来来自 agent 进程（**已验证**：回流 `process` 为解密层进程；回流连接的 `inboundName=lanflow-mitm-up`、`inboundUser=lanflow`），按程序分流失效，出口可能改变。M2：每个程序选择项独立 `IN-USER`，compose 把针对该程序的规则改写，如 `PROCESS-NAME,Foo,X` → `OR,((PROCESS-NAME,Foo),(AND,((IN-NAME,lanflow-mitm-up),(IN-USER,sel-foo)))),X`，出口与原来一致。用户显式固定上游出口时可改用 listener 的 `proxy:` |
| `find-process-mode` | 有程序选择项时强制 `strict` 或阻止（§4.1） |
| hosts 映射 | **已验证**：hosts 中的域名以 `CONNECT <IP>` 到达，回流 `host` 为空，订阅域名规则失效（`/etc/hosts` 经 `use-system-hosts` 同样）。M0 进一步确认：只要目标在命中抓包规则前已被解析（hosts，或规则链里先求值的 IP 类规则），都会这样。处理（已实现）：agent 本来就要按 `/connections` 做来源校验，同一条记录的 `metadata.host` 就是客户端请求的名字，回流改用它；查不到时按 IP 回流。不用 SNI 代替：redir-host / 客户端 DoH / 域前置下会改变实际目的地 |
| 嗅探器 | `override-destination` 开时 `host` 来自 SNI，与 agent 一致；嗅探关闭时 TUN redir-host 下按域名选择不生效（安全方向），UI 提示 |
| fake-ip | 域名天然可用；QUIC 按域名拒绝依赖 fake-ip 或 QUIC 嗅探 |
| aTrust 共存 | coexist 规则在最前，只保护已配置的 CorpDomains/CIDR；其余内网由内置旁路清单兜底；上游自签证书走重放透传 |
| 全局 / 直连模式 | 不走规则，显示暂停；规则仍在配置中，切回规则模式立即生效，与自动关闭计时联动。GLOBAL 风险见 §3.3 |
| IPv6 关闭 | 抓包规则在 `ipv6RejectRule` 之前，IPv6 字面地址会先被 agent 接收再在回流时被拒，客户端回落 IPv4 变慢。规则 ② 加 `NOT IP-CIDR6 ::/0` 或调整顺序（待决） |
| 配置重载 | 关闭顺序：① agent 标志立即置为透传 → ② 重新 compose 并 reload（出站保留到第 ③ 步完成）→ ③ `DELETE /connections/{id}` 关闭 `chains` 含解密出站的连接（必需，见 §2）→ ④ agent 停止监听。**待验证**：TUN 下 reload 是否重建 TUN——VM 中开 TUN 连续开关 20 次观察断流 |
| 连接页 / 统计 | 回流连接按 `inboundName=lanflow-mitm-up` 折叠到原始连接下（「已解密 · 回流经 X」）；`stats.go` 按 inbound 去重（M2） |
| 进程归属（M2） | **已验证**字段名：`metadata.inboundIP` / `inboundPort` / `inboundName` / `inboundUser`；mixed 入站的 CONNECT `type` 为 `HTTPS`。`find-process-mode: strict` 下只有规则里有进程类条件时才查进程，纯域名选择的记录没有进程名；macOS 上进程路径是规范路径（`/private/var/…` 而非 `/var/…`）。上游段精确：agent 拨回流时的本地端口 = `/connections` 中 `inboundName=lanflow-mitm-up` 的 `sourcePort`，得到节点链路与命中规则。下游段启发式：`chains` 含解密出站、`host`/端口一致、`start` 相差 < 2 s、FIFO 认领；进程不一致标「归属不确定」。查询 50–100 ms 去抖批量 `GET /connections`，复用 `internal/app/streams.go` 连接流，抓包期间强制前台频率。`controller.go` `Metadata` 补 `InboundName`/`InboundPort`/`InboundUser`（实测字段名） |

## 10. Agent / `lanflow ctl` / MCP 访问策略

已接入（`internal/rpc/levels.go`、`internal/rpc/redact.go`、`internal/rpc/guard.go`）：`CaptureStatus` 为读；`CaptureFlows`（L1 摘要，查询参数中的凭据打码，不含程序路径）与 `CaptureFlow`（L2：头打码为长度、正文只给结构）为敏感级，MCP 每个连接经窗口批准一次；开关、选择项、根证书操作为危险级（MCP 拒绝，ctl 每次确认），导出 CA 与 HAR 只在窗口。同一连接读过抓包数据后不能再写，写过后不能再读。返回值包在 `{"source":"lanflow-capture","note":…}` 里并标明是不可信数据。

| 级别 | 内容 | 条件 |
| --- | --- | --- |
| L0 状态 | 是否开启、选择项数量 | 默认可读 |
| L1 摘要 | 时间、程序、方法、主机、路径、状态、大小；无头无正文 | 设置中显式开启「允许 Agent 读取抓包摘要」 |
| L2 头与正文 | **已脱敏**，正文只给结构（键名、类型、长度），不给值 | 每个会话在 GUI 中逐次批准，带有效期 |

- 不提供原值级别，永不提供「复制为 cURL」（等于可重放的带 Cookie 请求）。
- Agent 不能开启解密、修改选择项、安装或导出 CA，这些只在 GUI 中操作。
- 读取抓包数据的通道与能执行动作的工具隔离，同一会话不能同时开启。
- 返回内容包裹为数据块并标明来源（正文是提示词注入的理想载体，这只是缓解）。
- 所有读取记审计日志，设置 › 数据可查看。M1 抓包期间不开放任何 Agent 访问。

## 11. 里程碑

单人估算，M0 到可发布的 M2 约 12–14 周（草案原估 9 周偏乐观）。代码总量约 6.5k 行（不含测试）：字节透传使解密层从约 1500 行降到约 800 行，子进程化（约 300–500 行）、引用清理与熔断抵消了这部分节省。

| 阶段 | 内容 | 估算 |
| --- | --- | --- |
| **M0 验证** | 内核语法、防环、inline provider、多端口、listener 口令、回环出站（非 TUN）、`/connections` 字段、reload 行为均**已完成**。剩余：TUN 回环出站可达与 TUN 下 reload 20 次开关（VM，`scripts/vm-e2e.sh`）；macOS 名称约束；QUIC 拒绝后 Chrome 回落耗时；GREASE ECH 识别与 ECH 安全降级；Chrome 预连接对固定检测的误判率；回环熔断 N/K | 1 周 |
| **M1 最小可用** | compose 抓包层（随机出站名、来源限定、引用清理、`exclude-filter`、GLOBAL 处理、黄金测试）；`lanflow capture-agent` 子进程（执行选择集、熔断、ClientHello 重放透传覆盖上游证书错误/mTLS/未知 ALPN/ECH、h1+h2 字节透传与旁路解析、内存环形缓冲）；CA（90 天、excluded 约束、用户域信任、Windows CNG、macOS 钥匙串、关闭 7 天自动移除）；**仅按域名、仅本机流量、仅规则模式**；自动关闭、指示、首次说明页；只读界面（列表、头、文本/JSON 正文）；diag/日志脱敏；**无 HAR / cURL 导出，无 Agent 访问**；HTTP/3 仅在 UI 说明 | 5–6 周 |
| **M2 程序选择与归因** | 按程序选择（`.app/*` 通配、`find-process-mode` 依赖）、每选择项独立口令、`PROCESS-*` 规则改写、进程归属关联、连接页折叠与统计去重、UDP 443 拒绝、hosts 回流修正、加密溢写、过滤搜索与更多查看器、复制原值确认 | 余下约 5–7 周 |
| **M3 导出与扩展** | HAR（默认无正文）与 cURL、WebSocket 帧、mTLS PKCS#12 导入、名称约束「严格模式」（仅域名选择，CA 带 `permitted`）、加密会话保存、敏感标记与审计高亮、Agent L1/L2 落地；硬件密钥（Secure Enclave）为研究项、uTLS 指纹为可选项 | 待 M2 后估算 |

## 12. 测试策略

- **单元**：`ca`（链、SAN、有效期、excluded 约束含 IP）；ClientHello 解析含 GREASE ECH、真实 ECH、无 SNI、截断包（`go test -fuzz`）；h1/h2 解析器（HPACK 动态表、截断、畸形帧只影响记录）；脱敏；`profile/capture` 黄金测试（§3.3 全部注入场景、规则顺序、`exclude-filter` 追加而非覆盖）。
- **集成（无系统信任）**：`httptest` 上游 + 测试 CA 注入上游 `RootCAs`，客户端信任澜流 CA。覆盖 h1、h2、gRPC 双向流、SSE、WebSocket、大正文截断；**字节一致性**：上游收到的请求与客户端发出的逐字节相同（头顺序、大小写、无新增 `Accept-Encoding`/`User-Agent`）；上游证书无效 → 客户端看到原证书；上游要求客户端证书 → 透传；客户端 alert 两次 → 跳过；零数据关闭不触发跳过。
- **e2e（`bin/mihomo`，沿用 `internal/app/live_e2e_test.go` / `live_sysproxy_test.go`）**：未选中零影响（证书指纹比对）；订阅注入 `MATCH,<出站>` 与包含出站的组后不进解密层、无回环；回流连接数 = 解密连接数；allow-lan 开/关时局域网来源不被解密；关闭后在途连接被断开、新连接拿到原证书；agent 被 kill 后选中应用恢复；hosts 映射域名回流修正。
- **VM（Parallels，`scripts/vm-e2e.sh` / `vm-tun-test.sh`）**：TUN 回环出站、TUN 下按程序选择 curl、20 次开关无断流、共享/网关时的来源限定。
- **平台手测**：macOS 用户域信任 + Safari/Chrome/Firefox；Windows CurrentUser Root + Edge/Chrome；拒绝授权、取消信任弹窗、MDM/GPO 下以叶子验证为准；Defender/VirusTotal。
- **性能**：在子进程上测 1000 并发 h2 流、100 MB 下载的吞吐与内存，确认 256 并发上限与 `GOMEMLIMIT` 生效。

## 13. 风险与待决问题

风险：

1. TUN 下回环出站与 reload 稳定性仍未实测（M0 首项）；若失败需重新评估回流方式。
2. 证书固定、Electron 主进程、rustls/Java 应用会让「已抓全」的印象失真；UI 必须列出透传与失败记录。
3. 下游进程归属是启发式，高并发同主机多程序时会出现「不确定」；根本解决需要 Mihomo 上游补丁（例如 http 出站支持带连接 ID 的头）。
4. ECH 普及后可解密比例下降；安全降级路径未验证。
5. macOS 未签名应用每次升级弹钥匙串授权，用户易养成「始终允许」；与签名计划一并解决。
6. 内置银行等域名清单需要长期维护。
7. 系统代理下 QUIC/UDP 不可见，审计不完整，需在 UI 诚实说明。

待决问题：

| 问题 | 选项 | 倾向 / 依据 |
| --- | --- | --- |
| 关闭解密时的信任策略默认值 | A：关闭 7 天后自动移除（本文默认）；B：关闭即移除，提供「保留信任」选项 | 评审两者皆可，A 少弹窗，B 暴露面更小 |
| agent 不可用时的兜底 | 把出站包进 `fallback` 组后备 DIRECT（应用不断网，但可能违背分流意图）/ 不兜底，靠 GUI 立即 reload | 默认不兜底，fallback 作为可选项是否提供待定 |
| IPv6 关闭时的规则位置 | 规则 ② 加 `NOT IP-CIDR6` / 关闭 IPv6 时把抓包规则排到 `ipv6RejectRule` 之后 | **已定**：规则 ② 加 `(NOT,((IP-CIDR6,::/0,no-resolve)))`，`mihomo -t` 通过，抓包仍排在自定义规则前（`compose_capture_test.go`） |
| 银行与支付域名 | 内置 Top 50 / 只在首次命中银行类域名时提示 | 待定 |
| 名称约束在 macOS 不生效时 | 仍保留 excluded（对 Chrome 等可能有效）/ 改为默认「严格模式」 | M0：SecTrust 对临时锚点执行约束，默认保留 excluded；钥匙串信任根的行为待 VM（§14.2 V3） |
| QUIC 拒绝方式 | `REJECT` / `REJECT-DROP` | 等 Chrome 回落耗时实测 |
| TUN 共享/网关时的非本机来源 | 排除规则的具体写法 | 等 VM 复现 |
| 回环熔断阈值 N、K | — | **已定**：首包指纹为主（TLS 一次、明文两次），速率兜底 N=1000 ms、K=100（§5.2） |
| 固定检测阈值 | 5 分钟 2 次是否合适 | 等预连接误判率实测 |

## 14. M0 验证结论

### 14.1 本机已完成（宿主机，全部 127.0.0.1 随机端口、临时 `-d` 目录、测试专用 CA，不碰 TUN / 系统代理 / DNS / 钥匙串信任）

| 项 | 方法 | 结论 |
| --- | --- | --- |
| 名称约束 | Go 签带 `excludedSubtrees`（域名 + `0.0.0.0/0`、`::/0`）的根，再签违反约束的叶子（子域、顶域、IP SAN）；分别用 Go `Verify`、`security verify-cert -c leaf -r root -p ssl`（临时锚点）、`/usr/bin/curl --cacert` 校验 | 三者都拒绝；curl 报 `excluded subtree violation`。合规叶子三者都通过 |
| 系统 curl | `curl -v` 连本地 TLS 服务 | 默认 LibreSSL + `/etc/ssl/cert.pem`，不读钥匙串；`CURL_SSL_BACKEND=secure-transport` 才用钥匙串 |
| Node | `tls.getCACertificates()` | Node 26.10：`NODE_USE_SYSTEM_CA=1` 生效 |
| `CONNECT <IP>` | e2e：hosts 映射的选中域名 | http 出站以 IP 发 CONNECT；`/connections` 的 `metadata.host` 仍是域名，agent 据此还原（已实现，e2e 覆盖） |
| `/connections` 字段 | e2e 打印 | `inboundIP/inboundPort/inboundName/inboundUser` 存在；回流连接 `inboundName=lanflow-mitm-up`、`inboundUser=lanflow`；mixed CONNECT 的 `type=HTTPS` |
| 进程查询 | e2e：`find-process-mode: strict` | 只有规则含进程条件时才查；`PROCESS-PATH-WILDCARD` 需写规范路径（`/private/var/…`） |
| 回环 | e2e：把回流 listener 的 `proxy:` 指向解密出站 | 不熔断：2 秒 5826 层；首包指纹熔断：第 3 个 accept 拒绝，请求快速失败（`TestM0LoopRate`，`LANFLOW_M0=1`） |
| ECH | e2e：Go 客户端 / 服务端真实 ECH（X25519 + AES-128-GCM，public name 与目标不同） | 外层 SNI ≠ 目标主机 → 透传，客户端 `ECHAccepted=true`、拿到原证书；GREASE 形态（有 0xfe0d、SNI = 目标）照常解密（`TestDecideGreaseECH`） |
| 规则语法 | `mihomo -t` | `(NOT,((IP-CIDR6,::/0,no-resolve)))` 嵌在 `AND` 中可用；`RULE-SET` 放在 `AND` 第一项，未选中连接不会再求值后面的 IP 类条件 |
| 上游证书无效 | e2e：自签上游 | 丢弃上游、重放 ClientHello 整体透传，客户端拿到原证书并完成握手 |

### 14.2 需要在虚拟机里验证（留给主会话，Parallels「macOS」VM，`scripts/vm-e2e.sh` 框架）

不在宿主机上做：宿主机开着 Clash Party，这些步骤要开 TUN 或改钥匙串信任。

- **V1 TUN 下回流可达**：VM 中开 TUN（`auto-detect-interface: true`），抓包选中 `example.com`，`curl https://example.com` → 应解密（证书颁发者为 Lanflow Local CA，`/connections` 有 `inboundName=lanflow-mitm-up` 的回流连接且最终出口为订阅节点）；`tcpdump -i utun1700 host 127.0.0.1` 应无回环流量进入 TUN。
- **V2 TUN 下 reload 20 次**：开 TUN 后连续开关解密 20 次（窗口里点，或 `lanflow ctl call SetCapture true/false`，危险级每次要在窗口确认），期间后台 `while curl -sf https://www.apple.com -o /dev/null; do :; done` 统计失败次数；记录每次 `PUT /configs` 是否重建 utun（`ifconfig` 设备序号是否变化）。
- **V3 钥匙串信任根与名称约束**：在「抓包」页「安装根证书」→ 登录钥匙串出现「Lanflow Local CA」且信任为「始终信任（SSL）」；用该 CA（导出私钥只在测试 VM 中做）签 `www.paypal.com` 与 IP 叶子，起本地 TLS 服务，Safari / Chrome / `security verify-cert -c leaf -p ssl`（不带 `-r`）分别访问，记录是否拒绝。
- **V4 QUIC 拒绝**（规则 ① 尚未实现，先手写一条规则）：TUN 下选中一个支持 HTTP/3 的站点，规则 ① `REJECT` 与 `REJECT-DROP` 各测一次，Chrome DevTools 看首次请求回落 TCP 的耗时。
- **V5 Chrome 预连接 / 固定误判**：Chrome 打开 10 个常用站点（只选中其中 3 个），1 小时内统计「零数据关闭」与证书 alert 次数，确认 5 分钟 2 次的阈值不误判。
- **V6 TUN + 共享 / 网关**：VM 开「互联网共享」，另一台虚拟机经它上网，访问选中域名应拿到原证书（agent 记录 `not-local`），据此确定规则 ② 的排除写法。
- **V7 ECH 安全降级**：Chrome / Firefox 访问 `crypto.cloudflare.com/cdn-cgi/trace`（`sni=encrypted`）时为 public name 签证书，看客户端是否关闭 ECH 重试；结论决定是否做「降级后解密」。
- **V8 整条链路（系统代理模式）**：VM 中装好 .app，「抓包」页阅读说明 → 安装根证书（应出现系统授权框，钥匙串「登录」中出现 Lanflow Local CA 且为「始终信任」）→ 连接页右键 `www.example.com`「解密此主机…」→ 开启。验证：Safari / Chrome 访问后「抓包」页出现记录，证书颁发者为 Lanflow；`curl https://www.apple.com` 证书仍为 Apple 原证书（内置跳过）；30 分钟后（或把自动关闭设为 1 分钟）自动关闭，侧栏与托盘指示消失，`lsof -iTCP -sTCP:LISTEN | grep lanflow` 中 agent 端口消失。再「移除根证书并删除私钥」，钥匙串中证书与「Lanflow Capture CA」密钥项都应消失。重新构建 .app（未签名）后再开启，记录钥匙串是否弹「允许访问」。

## 15. 实现状态

截至本节，M0、M1 完整，M2 的核心（按程序选择与精确归属）完成，M3 做了 HAR 导出与 WebSocket 帧。代码：

| 位置 | 内容 |
| --- | --- |
| `internal/profile/capture.go` | 抓包层组装、`scrubCaptureRefs`、`exclude-filter` 追加、有程序选择时把 `find-process-mode: off` 改为 `strict`；黄金测试 `compose_capture_test.go`（经 `mihomo -t`） |
| `internal/capture/ca` | 根证书（ECDSA P-256、90 天、`pathlen:0`、excluded 约束含全部 IP）、叶子 LRU 1024 + singleflight；`SecretKeys`（macOS 登录钥匙串，服务名 `Lanflow Capture CA`）、`keys_windows.go`（CNG 不可导出密钥） |
| `internal/capture/agent` | `lanflow capture-agent`：CONNECT 口令（常量时间）、ClientHello 解析（含模糊测试）、选择集自检、本机来源与进程归属（内核 `/connections`）、域名还原、ECH / GREASE 判定、证书固定临时跳过、首包指纹回环熔断 + 速率兜底、上游证书无效 / 要求客户端证书 / 任何上游握手失败时重放透传、h1 / h2 / WebSocket 旁路解析、环形缓冲 + 加密溢写、控制接口（127.0.0.1 + 随机 token） |
| `internal/capture/trust` | macOS `security add-trusted-cert -r trustRoot -p ssl -k login.keychain-db` / `remove-trusted-cert` / `delete-certificate`；Windows `CurrentUser\Root`；信任状态一律以叶子验证为准 |
| `internal/capture/redact`、`har` | 显示、HAR 与 agent 共用的打码；HAR 1.2（默认不含正文，`_lanflow` 扩展） |
| `internal/app/capture.go` | 选择项文件 `<DataDir>/capture/settings.json`（不进同步、不进备份）、agent 生命周期（崩溃按原端口重启，1 分钟内第 4 次退出则关闭解密并恢复分流）、开关顺序（agent 置透传 → reload → 关闭解密出站上的连接 → 停 agent）、30 分钟自动关闭（按墙钟，睡眠唤醒后立即关闭）、关闭 7 天或根证书过期后自动移除信任与私钥、GLOBAL 选中解密出站时重置为 DIRECT、代理页隐藏解密出站、诊断包隐藏选择项与内核日志中的解密行 |
| `internal/view/capture`、`conns`、`tray.go`、`shell*.go` | 「抓包」页（首次说明与确认、根证书横幅、选择编辑、列表 + 详情页签、正文查看器、默认打码、HAR 导出）；连接页行菜单「解密此主机… / 解密此程序…」；侧栏「解密中」徽标与状态行、托盘「HTTPS 解密」开关与标题、窗口标题「· 解密中」 |

测试：`internal/capture/agent/e2e_test.go` 用真实 `bin/mihomo`（127.0.0.1 随机端口、临时 `-d`）覆盖：未选中主机拿到原证书且不进 agent；选中主机 h1 / h2 解密与记录（正文、TLS、归属）且上游收到的字节与客户端逐字节相同；覆写引用解密出站被清除；回流 listener 指向解密出站时第 3 个 accept 即熔断；局域网来源被规则拦下，去掉规则的来源条件后由 agent 二次拒绝；关闭后在途解密请求被断开、新连接拿到原证书、内核中无残留解密连接；上游证书无效时原样透传；按程序选择时本进程与 `curl` 分别归属到各自的选择项；真实 ECH 透传、GREASE 照常解密。`internal/app/capture_test.go` 以测试二进制充当 agent 子进程（文件密钥库，不碰钥匙串）覆盖安装、开关、崩溃重启、关闭断连、自动关闭、7 天移除。

未做 / 与设计的差异：

- M2 未做：订阅中 `PROCESS-*` 规则对回流连接的改写（§9，解密连接的出口可能与未解密时不同）、连接页折叠回流连接与统计按 inbound 去重、选中目标 UDP 443 拒绝（规则 ①）、正文搜索与复制原值确认、multipart / 十六进制以外的更多查看器。
- M3 未做：cURL（按 §10 永不提供给 agent，窗口内也暂不做）、mTLS PKCS#12 导入、「严格模式」、加密会话保存、uTLS。
- 「证书固定」只按明确 alert 计数；「零数据关闭」未单独计数。
- `pprof`：agent 不开 pprof；GUI 的 `LANFLOW_PPROF` 在抓包期间未禁止（§8 表中一项）。
- 并发解密上限 256、agent `GOMEMLIMIT` 256 MiB 已设，性能项（1000 并发 h2 流、100 MB 下载）未测。

Windows 上未验证（只做了 `GOOS=windows go vet` 与交叉编译）：CNG 密钥的创建 / 打开 / 签名 / 删除（`keys_windows.go`）、`CurrentUser\Root` 的安装确认框与删除（`trust_windows.go`）、agent 子进程经命名管道访问内核控制器、`ca-remove` 卸载钩子（未实现：卸载程序不调用，需用户先在应用内移除）、Defender / SmartScreen 对 agent 的判定。

需要在虚拟机验证的步骤见 §14.2。
