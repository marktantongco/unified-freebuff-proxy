// hermes stealth sidecar — exposes the vendored @kori_xyz/hermes client over
// a tiny local HTTP API so the Go gateway (freebuff-unified) can route
// selected upstream requests through a real browser-like TLS 1.3 / HTTP2
// fingerprint.
//
// Endpoints:
//   GET    /healthz            -> liveness + version
//   POST   /v1/fetch           -> one-shot stealth request
//   POST   /v1/session/:id     -> session-scoped stealth request (cookie jar)
//   DELETE /v1/session/:id     -> drop a session cookie jar
//   GET    /v1/sessions        -> list active session ids
//
// Request body (POST):
// {
//   "url": "https://example.com",
//   "method": "GET",
//   "headers": {"x-custom": "1"},
//   "payload": {...} | "raw string",
//   "proxy": "socks5://user:pass@host:port" | null,   // optional override
//   "timeout_ms": 15000,
//   "http2": false,
//   "json": true    // when true, response.data is parsed as JSON if possible
// }
//
// Response: { status, headers, data, data_b64?, elapsed_ms }
// Binary bodies are base64-encoded into data_b64 (data is omitted then).

import http from "node:http";
import net from "node:net";
import hermes from "@kori_xyz/hermes";
const Session = hermes.Session;

const PORT = Number(process.env.HERMES_SIDECAR_PORT || 3101);
const HOST = process.env.HERMES_SIDECAR_HOST || "127.0.0.1";
const MAX_BODY = 8 * 1024 * 1024; // 8 MiB
const DEFAULT_TIMEOUT = 15_000;
// Long timeout for codebuff upstream completions (stream:false may buffer 3-5m).
const CODEBUFF_LONG_TIMEOUT = 280_000;

function isCodebuffURL(u) {
  try {
    const h = new URL(u).hostname;
    return h === "codebuff.com" || h.endsWith(".codebuff.com") || h === "www.codebuff.com";
  } catch { return false; }
}

/** @type {Map<string, Session>} */
const sessions = new Map();

function getSession(id) {
  let s = sessions.get(id);
  if (!s) {
    s = new Session();
    sessions.set(id, s);
  }
  s._lastUsed = Date.now();
  return s;
}

// Classify a proxy string into {scheme, rest}. `rest` strips the scheme.
// - socks5://...  -> handled locally via a real SOCKS5 handshake (hermes'
//   built-in proxyTunnel only speaks HTTP CONNECT and hangs SOCKS5 proxies)
// - http(s)://... or scheme-less -> passed to hermes (HTTP CONNECT tunnel)
function parseProxy(p) {
  if (p == null || p === "") return null;
  if (typeof p === "object") return { scheme: "object", rest: p };
  let s = String(p).trim();
  const m = s.match(/^(socks5h?|socks4a?|https?):\/\//i);
  if (m) {
    const scheme = m[1].toLowerCase().replace("socks5h", "socks5").replace("socks4a", "socks4");
    return { scheme, rest: s.slice(m[0].length) };
  }
  return { scheme: "http", rest: s }; // hermes treats scheme-less as CONNECT
}

function splitProxyAuth(rest) {
  let user, pass;
  let body = rest;
  const at = rest.lastIndexOf("@");
  if (at >= 0) {
    const cred = rest.slice(0, at);
    body = rest.slice(at + 1);
    const ci = cred.indexOf(":");
    user = ci >= 0 ? cred.slice(0, ci) : cred;
    pass = ci >= 0 ? cred.slice(ci + 1) : "";
  }
  const ci = body.lastIndexOf(":");
  const host = ci >= 0 ? body.slice(0, ci) : body;
  const port = ci >= 0 ? parseInt(body.slice(ci + 1), 10) || 1080 : 1080;
  return { host, port, user, pass };
}

// RFC 1928 SOCKS5 handshake against `proxyRest` ("[user:pass@]host:port"),
// tunneling to `host:port`. Resolves with the raw TCP socket so hermes can
// TLS-upgrade it (both its HTTP/1.1 Agent and http2.connect honor a
// pre-supplied `socket` option).
function socks5Connect(host, port, proxyRest, timeoutMs) {
  return new Promise((resolve, reject) => {
    const { host: pHost, port: pPort, user, pass } = splitProxyAuth(proxyRest);
    const sock = net.connect({ host: pHost, port: pPort || 1080 });
    sock.setTimeout(timeoutMs || 15000);
    let stage = "greet";
    let buf = Buffer.alloc(0);
    let settled = false;

    const fail = (err) => {
      if (settled) return;
      settled = true;
      sock.destroy();
      reject(err instanceof Error ? err : new Error(String(err)));
    };

    sock.on("timeout", () => fail(new Error("socks5 handshake timeout")));
    sock.on("error", (e) => fail(e));
    sock.on("close", () => {
      if (!settled) fail(new Error("socks5 proxy closed connection early"));
    });

    sock.on("connect", () => {
      const methods = user != null ? [0x00, 0x02] : [0x00];
      sock.write(Buffer.from([0x05, methods.length, ...methods]));
    });

    const sendConnect = () => {
      stage = "connect";
      const h = Buffer.from(host, "ascii");
      const req = Buffer.alloc(7 + h.length);
      req[0] = 0x05; // version
      req[1] = 0x01; // CONNECT
      req[2] = 0x00; // reserved
      req[3] = 0x03; // address type: domain name
      req[4] = h.length;
      h.copy(req, 5);
      req.writeUInt16BE(port, 5 + h.length);
      sock.write(req);
    };

    sock.on("data", (chunk) => {
      buf = Buffer.concat([buf, chunk]);
      if (stage === "greet") {
        if (buf.length < 2) return;
        const ver = buf[0];
        const method = buf[1];
        buf = buf.subarray(2);
        if (ver !== 0x05) return fail(new Error(`bad socks version ${ver}`));
        if (method === 0x00) return sendConnect();
        if (method === 0x02 && user != null) {
          stage = "auth";
          const u = Buffer.from(user);
          const p = Buffer.from(pass || "");
          sock.write(
            Buffer.concat([
              Buffer.from([0x01, u.length]),
              u,
              Buffer.from([p.length]),
              p,
            ])
          );
          return;
        }
        return fail(new Error(`socks5 method not acceptable: 0x${String(method)} `));
      }
      if (stage === "auth") {
        if (buf.length < 2) return;
        const ver = buf[0];
        const status = buf[1];
        buf = buf.subarray(2);
        if (ver !== 0x01 || status !== 0x00) {
          return fail(new Error(`socks5 auth failed (status ${status})`));
        }
        return sendConnect();
      }
      if (stage === "connect") {
        if (buf.length < 4) return;
        const atyp = buf[3];
        let need;
        if (atyp === 0x01) need = 4 + 4 + 2;
        else if (atyp === 0x04) need = 4 + 16 + 2;
        else if (atyp === 0x03) {
          if (buf.length < 5) return;
          need = 4 + 1 + buf[4] + 2;
        } else return fail(new Error(`bad socks5 address type ${atyp}`));
        if (buf.length < need) return;
        const rep = buf[1];
        buf = buf.subarray(need);
        if (rep !== 0x00) {
          return fail(new Error(`socks5 connect failed (reply 0x${rep.toString(16)})`));
        }
        settled = true;
        sock.setTimeout(0);
        sock.removeAllListeners("data");
        sock.removeAllListeners("timeout");
        sock.removeAllListeners("error");
        sock.removeAllListeners("close");
        resolve(sock);
      }
    });
  });
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (c) => {
      size += c.length;
      if (size > MAX_BODY) {
        reject(Object.assign(new Error("body too large"), { code: 413 }));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });
}

function send(res, code, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(code, { "Content-Type": "application/json" });
  res.end(body);
}

async function doStealth(store, params) {
  const started = Date.now();
  // Per-route timeout: codebuff upstream needs >5m for buffered completions;
  // probe targets (ipify/ipapi) keep short defaults.
  let defaultForURL = DEFAULT_TIMEOUT;
  if (params.url && isCodebuffURL(params.url)) defaultForURL = CODEBUFF_LONG_TIMEOUT;
  const opts = {
    url: params.url,
    method: (params.method || "GET").toUpperCase(),
    headers: params.headers || {},
    timeout: params.timeout_ms || defaultForURL,
    http2: Boolean(params.http2),
  };
  const parsed = parseProxy(params.proxy);
  if (parsed) {
    if (parsed.scheme === "socks5") {
      // Real SOCKS5: handshake ourselves, hand the raw socket to hermes.
      const target = new URL(params.url);
      const tPort = target.port
        ? Number(target.port)
        : target.protocol === "https:"
        ? 443
        : 80;
      opts.socket = await socks5Connect(
        target.hostname,
        tPort,
        parsed.rest,
        opts.timeout
      );
    } else if (parsed.scheme === "socks4") {
      throw Object.assign(new Error("socks4 proxies are not supported"), {
        code: 400,
      });
    } else if (parsed.scheme !== "object") {
      // http/https CONNECT proxy: hermes' built-in tunnel handles these.
      opts.proxy = parsed.rest;
    } else {
      opts.proxy = parsed.rest; // object pass-through
    }
  }
  if (params.payload !== undefined) opts.payload = params.payload;
  // hermes' HTTP/1.1 path replaces built headers wholesale when the caller
  // passes any headers object — keep Content-Length explicit for payloads
  // (chunked encoding is also a bot fingerprint browsers don't send).
  if (opts.payload !== undefined && !opts.headers["Content-Length"]) {
    opts.headers["Content-Length"] = String(
      Buffer.byteLength(
        typeof opts.payload === "string" || Buffer.isBuffer(opts.payload)
          ? opts.payload
          : JSON.stringify(opts.payload)
      )
    );
  }

  // Session instances expose .req(); the default export IS the request fn.
  const resp = store ? await store.req(opts) : await hermes(opts);

  let data = resp.data;
  let dataB64;
  if (Buffer.isBuffer(data)) {
    const ct = String(resp.headers?.["content-type"] || "");
    const isText =
      params.json === true ||
      ct.includes("json") ||
      ct.startsWith("text/") ||
      ct.includes("xml") ||
      ct === "";
    if (isText && params.json !== false) {
      const text = data.toString("utf8");
      if (params.json === true) {
        try {
          data = JSON.parse(text);
        } catch {
          data = text;
        }
      } else {
        data = text;
      }
    } else {
      dataB64 = data.toString("base64");
      data = undefined;
    }
  }

  // Normalize headers: HTTP2 pseudo-headers (e.g. ":status") can be numbers
  // and values can be arrays; stringify everything for stable JSON output.
  const flatHeaders = {};
  for (const [k, v] of Object.entries(resp.headers || {})) {
    flatHeaders[k] = Array.isArray(v) ? v.join(", ") : String(v);
  }

  // HTTP/2 responses may carry statusCode 0; the real status is in ":status".
  let status = resp.statusCode;
  if (!status && flatHeaders[":status"]) {
    status = Number(flatHeaders[":status"]) || 0;
  }

  return {
    status,
    headers: flatHeaders,
    http_version: `${resp.httpVersionMajor || 1}.${resp.httpVersionMinor || 0}`,
    data,
    data_b64: dataB64,
    elapsed_ms: Date.now() - started,
  };
}

// hermes' internal sockets can emit ECONNRESET/EPIPE on hostile upstreams;
// keep the sidecar alive and surface the failure as a 502 instead of dying.
process.on("uncaughtException", (err) => {
  console.error(`[hermes-sidecar] uncaught exception (kept alive): ${err?.message || err}`);
});
process.on("unhandledRejection", (reason) => {
  console.error(`[hermes-sidecar] unhandled rejection (kept alive): ${reason}`);
});

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host || "localhost"}`);
  const path = url.pathname;

  try {
    if (req.method === "GET" && path === "/healthz") {
      return send(res, 200, {
        status: "ok",
        service: "hermes-sidecar",
        hermes: "1.3.4",
        node: process.version,
        sessions: sessions.size,
        uptime_s: Math.round(process.uptime()),
      });
    }

    if (req.method === "GET" && path === "/v1/sessions") {
      return send(res, 200, { sessions: [...sessions.keys()] });
    }

    if (req.method === "DELETE") {
      const m = path.match(/^\/v1\/session\/(.+)$/);
      if (m) {
        const existed = sessions.delete(decodeURIComponent(m[1]));
        return send(res, 200, { deleted: existed });
      }
    }

    if (req.method === "POST") {
      const sessionMatch = path.match(/^\/v1\/session\/(.+)$/);
      const isSession = Boolean(sessionMatch);
      if (path !== "/v1/fetch" && !isSession) {
        return send(res, 404, { error: "unknown endpoint" });
      }

      const raw = await readBody(req);
      let params;
      try {
        params = JSON.parse(raw.toString("utf8") || "{}");
      } catch {
        return send(res, 400, { error: "invalid JSON body" });
      }
      if (!params.url) {
        return send(res, 400, { error: "url is required" });
      }

      const store = isSession ? getSession(decodeURIComponent(sessionMatch[1])) : null;
      try {
        const result = await doStealth(store, params);
        return send(res, 200, result);
      } catch (err) {
        // hermes errors carry the upstream response in `err.response` sometimes
        const status = err?.response?.statusCode;
        return send(res, 502, {
          error: String(err?.message || err),
          upstream_status: status ?? null,
        });
      }
    }

    return send(res, 404, { error: "unknown endpoint" });
  } catch (err) {
    const code = err?.code && Number.isInteger(err.code) ? err.code : 500;
    return send(res, code, { error: String(err?.message || err) });
  }
});

// Periodic session GC: drop cookie jars idle for > 30 min.
const SESSION_TTL_MS = 30 * 60 * 1000;
setInterval(() => {
  const now = Date.now();
  for (const [id, s] of sessions) {
    if (s._lastUsed && now - s._lastUsed > SESSION_TTL_MS) sessions.delete(id);
  }
}, 5 * 60 * 1000).unref();

server.listen(PORT, HOST, () => {
  console.log(`[hermes-sidecar] listening on http://${HOST}:${PORT} (node ${process.version})`);
});

for (const sig of ["SIGINT", "SIGTERM"]) {
  process.on(sig, () => {
    console.log(`[hermes-sidecar] ${sig}, closing`);
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 3000).unref();
  });
}
