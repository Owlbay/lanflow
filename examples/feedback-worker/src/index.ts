// A minimal Cloudflare Worker receiving Lanflow feedback reports.
// Contract: docs/feedback-api.md in the Lanflow repository.
//
//   POST /v1/reports
//     application/json            the report metadata
//     multipart/form-data         "report" (JSON) + "diagnostics" (diagnostics.tar.gz)
//
//   201 {id, url?}   stored        200 {id, url?}   already stored (deduplicated by ID)
//   400 / 401 / 413               refused, the client does not retry
//   429 + Retry-After             rate limited per client IP, the client waits and retries
//
// Metadata goes to KV (report:<id>, with a TTL), the diagnostics bundle to
// R2 (reports/<yyyy-mm-dd>/<id>.tar.gz).

export interface Env {
  REPORTS: KVNamespace;
  DIAGNOSTICS: R2Bucket;
  REPORT_TOKEN?: string; // secret; when set, Authorization: Bearer <token> is required
  MAX_BODY_BYTES?: string;
  REPORTS_PER_HOUR?: string;
  RETENTION_DAYS?: string;
  VIEW_BASE_URL?: string;
}

interface ReportMeta {
  schema: number;
  id: string;
  kind: "manual" | "auto";
  createdAt: string;
  description: string;
  contact?: string;
  app: { version: string; os: string; arch: string; osVersion?: string; core?: string; language?: string };
  errors: unknown[];
  log?: string;
  attachments: { name: string; size: number }[];
}

interface StoredReport {
  meta: ReportMeta;
  receivedAt: string;
  clientVersion: string | null;
  diagnosticsKey?: string;
  diagnosticsSize?: number;
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const MAX_DESCRIPTION = 10000;
const MAX_ERRORS = 200;

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json; charset=utf-8", ...headers },
  });
}

const fail = (status: number, error: string, headers?: Record<string, string>) => json(status, { error }, headers);

function num(v: string | undefined, fallback: number): number {
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

// Constant-time comparison of two strings.
function safeEqual(a: string, b: string): boolean {
  const ea = new TextEncoder().encode(a);
  const eb = new TextEncoder().encode(b);
  let diff = ea.length ^ eb.length;
  for (let i = 0; i < Math.max(ea.length, eb.length); i++) {
    diff |= (ea[i] ?? 0) ^ (eb[i] ?? 0);
  }
  return diff === 0;
}

function authorized(req: Request, env: Env): boolean {
  if (!env.REPORT_TOKEN) return true;
  const h = req.headers.get("authorization") ?? "";
  const m = /^Bearer\s+(.+)$/i.exec(h);
  return m !== null && safeEqual(m[1].trim(), env.REPORT_TOKEN);
}

// A fixed one-hour window per client IP, counted in KV. KV is eventually
// consistent, so the limit is approximate; use the Rate Limiting binding
// for a strict one.
async function rateLimited(req: Request, env: Env): Promise<number> {
  const ip = req.headers.get("cf-connecting-ip") ?? "unknown";
  const now = Math.floor(Date.now() / 1000);
  const hour = Math.floor(now / 3600);
  const key = `rl:${ip}:${hour}`;
  const count = Number((await env.REPORTS.get(key)) ?? "0");
  if (count >= num(env.REPORTS_PER_HOUR, 10)) {
    return (hour + 1) * 3600 - now; // seconds until the window resets
  }
  await env.REPORTS.put(key, String(count + 1), { expirationTtl: 3600 + 60 });
  return 0;
}

function validMeta(m: unknown, id: string): m is ReportMeta {
  if (typeof m !== "object" || m === null) return false;
  const r = m as Partial<ReportMeta>;
  return (
    r.schema === 1 &&
    r.id === id &&
    (r.kind === "manual" || r.kind === "auto") &&
    typeof r.createdAt === "string" &&
    typeof r.description === "string" &&
    r.description.length <= MAX_DESCRIPTION &&
    typeof r.app === "object" &&
    r.app !== null &&
    Array.isArray(r.errors) &&
    r.errors.length <= MAX_ERRORS &&
    Array.isArray(r.attachments)
  );
}

function answer(id: string, env: Env): { id: string; url?: string } {
  return env.VIEW_BASE_URL ? { id, url: env.VIEW_BASE_URL + id } : { id };
}

async function handleReport(req: Request, env: Env): Promise<Response> {
  if (!authorized(req, env)) return fail(401, "missing or wrong token");

  const maxBody = num(env.MAX_BODY_BYTES, 10 << 20);
  const declared = Number(req.headers.get("content-length") ?? "0");
  if (declared > maxBody) return fail(413, `body larger than ${maxBody} bytes`);

  const id = req.headers.get("x-lanflow-report-id") ?? "";
  if (!UUID_RE.test(id)) return fail(400, "X-Lanflow-Report-Id must be a UUID");

  // Retries and repeated sends carry the same ID: answer 200, store nothing.
  if ((await env.REPORTS.get(`report:${id}`)) !== null) return json(200, answer(id, env));

  const wait = await rateLimited(req, env);
  if (wait > 0) return fail(429, "too many reports, try again later", { "retry-after": String(wait) });

  // Read the body once, enforcing the limit even without Content-Length.
  const body = await req.arrayBuffer();
  if (body.byteLength > maxBody) return fail(413, `body larger than ${maxBody} bytes`);

  const type = req.headers.get("content-type") ?? "";
  let metaText: string;
  let diagnostics: ArrayBuffer | null = null;
  if (type.startsWith("application/json")) {
    metaText = new TextDecoder().decode(body);
  } else if (type.startsWith("multipart/form-data")) {
    let form: FormData;
    try {
      form = await new Response(body, { headers: { "content-type": type } }).formData();
    } catch {
      return fail(400, "malformed multipart body");
    }
    const report = form.get("report");
    const diag = form.get("diagnostics");
    if (report === null) return fail(400, "missing report part");
    metaText = typeof report === "string" ? report : await report.text();
    if (diag !== null && typeof diag !== "string") {
      diagnostics = await diag.arrayBuffer();
      const head = new Uint8Array(diagnostics, 0, Math.min(2, diagnostics.byteLength));
      if (head[0] !== 0x1f || head[1] !== 0x8b) return fail(400, "diagnostics must be gzip");
    }
  } else {
    return fail(400, "unsupported content type");
  }

  let meta: unknown;
  try {
    meta = JSON.parse(metaText);
  } catch {
    return fail(400, "report is not JSON");
  }
  if (!validMeta(meta, id)) return fail(400, "report does not follow schema 1 or its id differs from the header");

  const retention = num(env.RETENTION_DAYS, 90) * 86400;
  const stored: StoredReport = {
    meta,
    receivedAt: new Date().toISOString(),
    clientVersion: req.headers.get("x-lanflow-version"),
  };
  if (diagnostics !== null) {
    const day = stored.receivedAt.slice(0, 10);
    stored.diagnosticsKey = `reports/${day}/${id}.tar.gz`;
    stored.diagnosticsSize = diagnostics.byteLength;
    await env.DIAGNOSTICS.put(stored.diagnosticsKey, diagnostics, {
      httpMetadata: { contentType: "application/gzip" },
      customMetadata: { reportId: id, kind: meta.kind },
    });
  }
  // The metadata is written last: a report counts as stored once its KV
  // entry exists, so a failed R2 write is simply retried by the client.
  await env.REPORTS.put(`report:${id}`, JSON.stringify(stored), { expirationTtl: retention });
  return json(201, answer(id, env));
}

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const url = new URL(req.url);
    if (url.pathname !== "/v1/reports") return fail(404, "not found");
    if (req.method !== "POST") return fail(405, "use POST", { allow: "POST" });
    try {
      return await handleReport(req, env);
    } catch (e) {
      // 5xx: the client retries with backoff
      return fail(500, e instanceof Error ? e.message : "internal error");
    }
  },
} satisfies ExportedHandler<Env>;
