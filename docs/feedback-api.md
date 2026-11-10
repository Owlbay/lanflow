# 反馈上报接口约定

澜流的「反馈问题」把报告发送到**用户自己部署的服务**（例如 Cloudflare Worker）。客户端不写死任何服务器：上报地址在 设置 › 关于 › 反馈上报 中填写，默认为空；为空时报告只能保存到文件，再自行发送。本文是服务端需要实现的接口，参考实现见 [`examples/feedback-worker/`](../examples/feedback-worker/)。

客户端实现：`internal/app/feedback.go`（生成与发送）、`internal/app/feedback_redact.go`（报告专用的脱敏）、`internal/applog/errors.go`（本地错误收集）、`internal/view/feedback.go`（对话框）。

## 1. 客户端行为概览

- **本地错误收集**：日志目录下的 `errors.jsonl`（macOS `~/Library/Logs/Lanflow/`，Windows `%LOCALAPPDATA%\Lanflow\Logs\`），每行一条 [错误记录](#33-错误记录)：澜流运行日志的 Error 级条目、内核异常退出（退出码与内核最后 20 行日志）、Go panic（含调用栈）。超过 500 条或 512 KB 轮转为 `errors.jsonl.1`，只保留一份旧文件。只在本机，不会自动发出（除非用户开启「出错时自动上报」）。
- **崩溃**：主 goroutine 和后台 goroutine 的入口 recover，写入记录后照常崩溃；运行时无法 recover 的致命错误由 `debug.SetCrashOutput` 写入 `crash.txt`。下次启动时提示「上次异常退出，是否反馈？」。
- **手动反馈**：用户填写描述、可选联系方式，勾选是否附带诊断包和最近的运行日志 → 客户端生成报告并**完整展示将要发送的内容**（JSON 全文，附件逐个列出文件名、大小，可展开查看全文）→ 用户点「发送」才发出，或「保存到文件」。
- **自动上报**（默认关闭）：开启且设置了上报地址时，新的错误记录在 30 秒内合并成一份 `kind: "auto"` 报告发送，每 10 分钟至多一份；只含错误摘要，**不含诊断包和日志**，描述为空。
- **本机接口 / MCP**：agent 可以用 `BuildFeedback` 起草报告（读权限），`SendFeedback` 只供澜流界面调用，发送必须由用户在界面确认。

## 2. 请求

```
POST {endpoint}/v1/reports
```

`{endpoint}` 是用户填写的上报地址去掉末尾 `/`，例如 `https://lanflow-feedback.example.workers.dev` → `https://lanflow-feedback.example.workers.dev/v1/reports`。必须是 `https://`（`http://localhost` / `127.0.0.1` 仅供调试）。

### 2.1 请求头

| 请求头 | 说明 |
| --- | --- |
| `Content-Type` | `application/json`（无附件）或 `multipart/form-data; boundary=…`（有诊断包） |
| `X-Lanflow-Version` | 客户端版本，例如 `0.1.1`；开发构建为 `dev` |
| `X-Lanflow-Report-Id` | 报告 ID（UUID v4），与请求体里的 `id` 相同。**服务端以它去重**：同一份报告重试或重复点发送时 ID 不变 |
| `Authorization` | 可选，`Bearer <token>`。token 由用户在设置里填写，存于系统钥匙串；服务端配置了 token 时应校验 |
| `User-Agent` | `Lanflow/<版本>` |

### 2.2 请求体：JSON（无附件）

整个请求体就是下面的报告元数据。

### 2.3 请求体：multipart（有诊断包）

| 字段名 | 文件名 | Content-Type | 内容 |
| --- | --- | --- | --- |
| `report` | `report.json` | `application/json` | 报告元数据，与 2.2 相同 |
| `diagnostics` | `diagnostics.tar.gz` | `application/gzip` | 诊断包：tar + gzip，文件即元数据 `attachments` 中列出的那些，路径相同 |

两个部分按此顺序出现。服务端应校验 `diagnostics` 的大小，并可以按 `attachments` 核对内容。

## 3. 报告元数据

```json
{
  "schema": 1,
  "id": "5f0c2b7e-3a1d-4c8e-9b6a-2d4f1e8c7a90",
  "kind": "manual",
  "createdAt": "2026-11-10T12:00:00Z",
  "description": "开启 TUN 后部分网站打不开",
  "contact": "me@example.com",
  "app": {
    "version": "0.1.1",
    "os": "darwin",
    "arch": "arm64",
    "osVersion": "15.5",
    "core": "v1.19.15",
    "language": "zh"
  },
  "errors": [
    {
      "time": "2026-11-10T11:58:03.123+08:00",
      "kind": "core-exit",
      "message": "Mihomo 内核意外退出（exit status 1）",
      "detail": "time=\"…\" level=fatal msg=\"Parse config error: …\"",
      "exitCode": 1,
      "version": "0.1.1",
      "platform": "darwin/arm64"
    }
  ],
  "log": "2026-11-10 11:57:59.001 INFO    start core: ok\n…",
  "attachments": [
    { "name": "system.txt", "size": 2048 },
    { "name": "config/runtime.yaml", "size": 10240 }
  ]
}
```

### 3.1 字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `schema` | number | 格式版本，当前为 `1`。不认识的版本可以拒绝（400）或原样保存 |
| `id` | string | 报告 ID，UUID v4，等于 `X-Lanflow-Report-Id` |
| `kind` | string | `manual`（用户手动反馈）或 `auto`（出错自动上报） |
| `createdAt` | string | 生成时间，RFC 3339，UTC，精确到秒 |
| `description` | string | 用户的描述，已脱敏，至多 10000 字符；`auto` 时为空 |
| `contact` | string? | 可选联系方式，至多 200 字符，原样（用户自己填写） |
| `app` | object | 见 3.2 |
| `errors` | array | 最近的错误记录，旧的在前；手动反馈默认至多 50 条，自动上报至多 20 条（只含上次上报之后的） |
| `log` | string? | 最近一段 `lanflow.log`（至多 256 KB，已脱敏）；用户未勾选或自动上报时不出现 |
| `attachments` | array | 诊断包中的文件 `{name, size}`，`size` 为解压后的字节数；无诊断包时为 `[]` |

### 3.2 `app`

| 字段 | 说明 |
| --- | --- |
| `version` | 澜流版本 |
| `os` / `arch` | Go 的 `GOOS` / `GOARCH`：`darwin`、`windows`；`arm64`、`amd64` |
| `osVersion` | 系统版本，可能为空 |
| `core` | 当前 Mihomo 内核版本，可能为空 |
| `language` | 界面语言 `zh` / `en` |

### 3.3 错误记录

`errors.jsonl` 的每一行，也是 `errors` 数组的元素。

| 字段 | 说明 |
| --- | --- |
| `time` | 发生时间，RFC 3339（本地时区） |
| `kind` | `error`：运行日志的 Error 级条目；`core-exit`：内核异常退出；`panic`：Go panic 或致命运行时错误 |
| `message` | 单行消息，已脱敏，至多 2 KB |
| `detail` | 可选：panic 的调用栈，或内核最后若干行日志；至多 16 KB |
| `exitCode` | 可选，仅 `core-exit`，内核退出码（被信号终止时没有） |
| `version` / `platform` | 记录时的澜流版本，`GOOS/GOARCH` |

### 3.4 诊断包内容

与「导出诊断信息」同源，但更严格（`diagFiles(feedback=true)`）：

| 文件 | 内容 |
| --- | --- |
| `README.txt` | 说明 |
| `system.txt` | 应用与内核状态、局域网 IP、网关、系统 DNS、订阅名称与链接主机名（不含路径与令牌） |
| `config/settings.json` | 澜流设置，去掉不走代理列表、检测站点、Wi-Fi 规则、aTrust 列表、WebDAV、代理认证、界面状态等，只留条数 |
| `config/runtime.yaml`、`config/override-*.yaml` | 运行时配置与覆写：节点服务器、密码、UUID、密钥去除；`rules`、`rule-providers`、`hosts`、`nameserver-policy`、`fake-ip-filter` 等只留条数 |
| `logs/mihomo*.log` | 内核日志中 warning / error / fatal 级的行，**不含任何连接行**（`来源 --> 目标`） |
| `logs/lanflow.log*` | 仅当用户勾选「附带最近的澜流运行日志」 |
| `logs/helper*.log` | TUN 辅助程序日志 |
| `network/*` | 路由表、DNS 解析器、系统代理设置 |

每个日志至多取最后 512 KB。

## 4. 响应

响应体为 JSON。

| 状态码 | 含义 | 响应体 | 客户端行为 |
| --- | --- | --- | --- |
| `201 Created` | 新报告已保存 | `{"id": "<报告 ID>", "url": "<可选，查看地址>"}` | 成功，显示「反馈已发送」，有 `url` 时一并显示 |
| `200 OK` | 该 ID 已经收到过（去重） | 同上 | 成功，显示「这份反馈之前已发送过」 |
| `400` | 请求格式错误 | `{"error": "…"}` | 不重试，显示错误 |
| `401` / `403` | token 缺失或错误 | `{"error": "…"}` | 不重试 |
| `413` | 超过大小上限 | `{"error": "…"}` | 不重试；用户可取消勾选附件后重新生成 |
| `429 Too Many Requests` | 限流 | `{"error": "…"}`，带 `Retry-After`（秒数或 HTTP 日期） | 等 `Retry-After` 后重试；超过 60 秒则放弃并提示「请在 X 后重试」 |
| `5xx` | 服务端错误 | 任意 | 退避重试 |

**重试**：一次发送至多 4 次请求。网络错误和 5xx 依次等待 1、2、4 秒；429 等 `Retry-After`。所有重试使用同一个报告 ID 和完全相同的请求体，所以服务端的去重保证不会重复入库。直连失败且内核在运行时，下一次改经本机代理端口发送（上报地址可能被墙）。

## 5. 大小上限

| 项目 | 上限 |
| --- | --- |
| 整个请求体 | 客户端生成时保证 ≤ 8 MB，超过会要求用户少附带内容；服务端建议拒绝 > 10 MB（413） |
| `description` | 10000 字符 |
| `contact` | 200 字符 |
| `errors` | 200 条；单条 `message` 2 KB、`detail` 16 KB |
| `log` | 256 KB |
| 诊断包中每个日志 | 最后 512 KB |

Cloudflare Workers 免费版请求体上限为 100 MB，KV 单个值上限 25 MB，R2 无此限制；按上表设置自己的上限即可。

## 6. 隐私

客户端在**生成报告时**就完成脱敏，预览里看到的就是将要发送的全部内容：

- 链接只保留协议和主机名，路径与查询参数（订阅令牌所在）替换为 `***`；`token=`、`password=`、`secret=`、`uuid=` 等形式的值掩码（`applog.Redact`）。
- 配置中的节点服务器、认证信息、UUID、密钥、证书全部替换为 `***`。
- **公网 IP**（IPv4 与 IPv6）替换为 `x.x.x.x` / `x:x:x:x`；局域网、回环、链路本地、CGNAT、Mihomo fake-ip（198.18.0.0/15）地址保留，便于排查局域网问题。
- 不收集连接明细、访问过的域名、规则与域名列表、Wi-Fi 名称、订阅链接。
- 访问令牌（`Authorization`）只在请求头里，不进入报告。
- `contact` 是用户主动填写的，会原样发送。
- 用户的描述也经过同样的脱敏（误贴的订阅链接会被截掉）。

服务端仍应把报告当作个人数据对待：只允许自己访问，设置保留期限，不公开 `url`。

## 7. 服务端建议

- **鉴权**：配置一个随机 token（`wrangler secret put REPORT_TOKEN`），校验 `Authorization: Bearer`，用常量时间比较。不配置 token 时任何人都能提交，务必配合限流。
- **限流**：按客户端 IP（`CF-Connecting-IP`）限流，例如每 IP 每小时 10 份；超过返回 429 和 `Retry-After`。Cloudflare 的 [Rate Limiting 绑定](https://developers.cloudflare.com/workers/runtime-apis/bindings/rate-limit/) 或 KV 计数都可以。
- **去重**：以 `X-Lanflow-Report-Id` 为键；已存在时返回 200 和同样的 `{id, url}`，不再写入。
- **校验**：`id` 是 UUID 且等于请求头；`schema` 为 1；`kind` 为 `manual`/`auto`；大小在上限内；multipart 时 `diagnostics` 是 gzip。
- **存储**：元数据（JSON，通常几 KB）存 KV，键 `report:<id>`；诊断包存 R2，键 `reports/<yyyy-mm-dd>/<id>.tar.gz`。也可以只用 R2 或 D1。
- **保留期限**：KV 写入时设 `expirationTtl`（例如 90 天）；R2 设置生命周期规则自动删除同样期限之前的对象。
- **通知**（可选）：收到 `manual` 报告时转发到邮件、Telegram 或 GitHub Issue，只发描述与错误摘要，不发诊断包。
- **查看**：`url` 可以指向一个需要登录（Cloudflare Access）的查看页面；不需要时省略。

## 8. 兼容性

- 新增字段只会以可选字段出现；服务端应忽略不认识的字段。
- 不兼容的修改会提升 `schema`，并在本文记录。
