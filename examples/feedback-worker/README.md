# 反馈上报 Worker 示例

一个最小的 Cloudflare Worker，按 [`docs/feedback-api.md`](../../docs/feedback-api.md) 接收澜流的反馈报告。只是示例：澜流仓库不会部署它，也不会替你创建任何 Cloudflare 资源。

它做的事：

- `POST /v1/reports`，接受 JSON 或 multipart（`report` + `diagnostics.tar.gz`）。
- 配置了 `REPORT_TOKEN` 时校验 `Authorization: Bearer <token>`（常量时间比较）。
- 按 `Content-Length` 和实际读到的字节数检查大小（默认 10 MB），超过返回 413。
- 按 `X-Lanflow-Report-Id` 去重：已存在返回 200，不再写入。
- 按客户端 IP 每小时限流（默认 10 份，KV 计数，近似），超过返回 429 和 `Retry-After`。
- 元数据存 KV（`report:<id>`，默认保留 90 天），诊断包存 R2（`reports/<日期>/<id>.tar.gz`）。

## 部署

需要 Node.js 和一个 Cloudflare 账号。以下命令都在这个目录里运行，会在**你自己的**账号下创建资源：

```bash
npm install
npx wrangler login
npx wrangler kv namespace create REPORTS      # 把输出的 id 填进 wrangler.toml
npx wrangler r2 bucket create lanflow-feedback
npx wrangler secret put REPORT_TOKEN          # 输入一个随机字符串，例如 openssl rand -hex 24 的输出
npx wrangler deploy
```

部署后得到形如 `https://lanflow-feedback.<子域>.workers.dev` 的地址。在澜流 设置 › 关于 › 反馈上报 › 配置… 中：

- 上报地址：填这个地址（不带 `/v1/reports`）。
- 访问令牌：填 `REPORT_TOKEN` 的值。

然后用「反馈问题…」发一份试试。

## 保留期限

KV 元数据按 `RETENTION_DAYS` 自动过期。R2 不会自动删除，请在 Cloudflare 控制台为桶添加生命周期规则（例如删除 90 天前的对象），或：

```bash
npx wrangler r2 bucket lifecycle add lanflow-feedback --expire-days 90
```

## 查看报告

```bash
npx wrangler kv key list --binding REPORTS --prefix report:
npx wrangler kv key get --binding REPORTS "report:<id>"
npx wrangler r2 object get "lanflow-feedback/reports/<日期>/<id>.tar.gz" --file report.tar.gz
tar -xzf report.tar.gz -C report/
```

想在澜流里显示查看链接，可以设置 `VIEW_BASE_URL`（例如一个用 Cloudflare Access 保护的页面），Worker 会返回 `url = VIEW_BASE_URL + id`。

## 本地调试

```bash
npx wrangler dev
curl -i http://localhost:8787/v1/reports \
  -H 'content-type: application/json' \
  -H 'x-lanflow-report-id: 5f0c2b7e-3a1d-4c8e-9b6a-2d4f1e8c7a90' \
  -H 'authorization: Bearer <token>' \
  -d '{"schema":1,"id":"5f0c2b7e-3a1d-4c8e-9b6a-2d4f1e8c7a90","kind":"manual","createdAt":"2026-11-10T12:00:00Z","description":"test","app":{"version":"dev","os":"darwin","arch":"arm64"},"errors":[],"attachments":[]}'
```

再发一次同样的请求会得到 200（去重）。澜流里也可以把上报地址设为 `http://localhost:8787` 直接测试（仅 localhost 允许 http）。

## 局限

- KV 是最终一致的：限流计数和去重在并发请求下是近似的。需要严格限流时改用 [Rate Limiting 绑定](https://developers.cloudflare.com/workers/runtime-apis/bindings/rate-limit/)，需要严格去重时改用 D1 或 Durable Object。
- 没有查看界面和通知；可以在 `handleReport` 末尾把 `meta.description` 和 `meta.errors` 转发到邮件或 Telegram。
