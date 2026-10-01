export type McpHttpUrlKind = "loopback" | "remote";

export type McpHttpUrlCode =
  | "empty"
  | "invalid_url"
  | "remote_requires_https"
  | "project_remote_forbidden";

export type McpHttpUrlOk = {
  ok: true;
  url: string;
  kind: McpHttpUrlKind;
};

export type McpHttpUrlErr = {
  ok: false;
  code: McpHttpUrlCode;
};

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "::1"]);

function isLoopbackHost(host: string): boolean {
  // IPv6 hostname includes brackets.
  const lowered = host.trim().toLowerCase().replace(/^\[(.*)\]$/, "$1");
  if (LOOPBACK_HOSTS.has(lowered)) return true;
  if (lowered.startsWith("127.")) {
    const parts = lowered.split(".");
    if (parts.length === 4 && parts.every((p) => /^\d+$/.test(p))) {
      const n = parts.map(Number);
      return n[0] === 127 && n.every((v) => v >= 0 && v <= 255);
    }
  }
  return false;
}

function bracketBareIPv6(raw: string): string {
  if (raw === "::1") return "[::1]";
  if (raw.startsWith("::1/") || raw.startsWith("::1?")) {
    return `[::1]${raw.slice(3)}`;
  }
  return raw;
}

function canonicalURL(u: URL): string {
  const path = u.pathname === "/" ? "" : u.pathname;
  return `${u.protocol}//${u.host}${path}${u.search}${u.hash}`;
}

function finish(href: string): McpHttpUrlOk | McpHttpUrlErr {
  let parsed: URL;
  try {
    parsed = new URL(href);
  } catch {
    return { ok: false, code: "invalid_url" };
  }
  if (parsed.username || parsed.password || !parsed.hostname) {
    return { ok: false, code: "invalid_url" };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { ok: false, code: "invalid_url" };
  }
  return {
    ok: true,
    url: canonicalURL(parsed),
    kind: isLoopbackHost(parsed.hostname) ? "loopback" : "remote",
  };
}

// Schemeless host:port is http on loopback and https otherwise.
export function parseMcpHttpUrl(raw: string): McpHttpUrlOk | McpHttpUrlErr {
  const trimmed = raw.trim();
  if (!trimmed) return { ok: false, code: "empty" };
  if (trimmed.startsWith("/")) return { ok: false, code: "invalid_url" };
  if (trimmed.includes("://")) {
    return finish(trimmed);
  }
  const parsed = finish(`http://${bracketBareIPv6(trimmed)}`);
  if (!parsed.ok || parsed.kind === "loopback") return parsed;
  return finish(parsed.url.replace(/^http:/, "https:"));
}

export function classifyMcpHttpUrl(
  raw: string,
  projectScope: boolean,
): McpHttpUrlOk | McpHttpUrlErr {
  const parsed = parseMcpHttpUrl(raw);
  if (!parsed.ok) return parsed;
  if (parsed.kind === "remote" && new URL(parsed.url).protocol === "http:") {
    return { ok: false, code: "remote_requires_https" };
  }
  if (projectScope && parsed.kind === "remote") {
    return { ok: false, code: "project_remote_forbidden" };
  }
  return parsed;
}
