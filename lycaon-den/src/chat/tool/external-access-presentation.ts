import type {
  ExternalAccess,
  ExternalAccessDetection,
  ExternalAccessEndpoint,
  ExternalAccessMode,
  ExternalAccessSocket,
  ExternalAccessVisibilitySummary,
} from "../../api/types.ts";

/** Transcript label for external access receipts. */
export const EXTERNAL_ACCESS_LABEL = "External access";

export type ExternalAccessView = {
  summary: string;
  visibilitySummary: ExternalAccessVisibilitySummary;
  /** Screen-reader / aria description of overall visibility. */
  visibilityAnnouncement: string;
  endpoints: ExternalAccessEndpointRow[];
  sockets: ExternalAccessSocketRow[];
  declared: string[];
  direct: boolean;
  fullBypass: boolean;
  detections: ExternalAccessDetectionRow[];
  /** Visibility gaps and direct access remain visible without mediated hosts. */
  mustShow: boolean;
};

export type ExternalAccessEndpointRow = {
  host: string;
  port: number;
  transport: string;
  decision: "allow" | "deny";
  attemptCount: number;
  label: string;
  visibilityLabel: string;
};

export type ExternalAccessSocketRow = {
  pathLabel: string;
  scope: string;
  authorityLabel: string;
  visibilityLabel: string;
};

export type ExternalAccessDetectionRow = {
  title: string;
  level: string;
  citation: string;
  incompleteNote: string;
};

/** Builds the external access view from the host receipt. */
export function buildExternalAccessView(input: {
  externalAccess?: ExternalAccess | null;
}): ExternalAccessView | null {
  const ea = input.externalAccess ?? null;
  return ea ? viewFromExternalAccess(ea) : null;
}

function viewFromExternalAccess(ea: ExternalAccess): ExternalAccessView {
  const modes = new Set<ExternalAccessMode>(ea.modes ?? []);
  const endpoints = (ea.endpoints ?? []).map(endpointRow);
  const sockets = (ea.sockets ?? []).map(socketRow);
  const declared = (ea.declared_destinations ?? []).filter((d) => d.trim());
  const direct = !!ea.direct || modes.has("direct_ip");
  const fullBypass = modes.has("full_bypass");
  const detections = (ea.detections ?? []).map(detectionRow);
  const visibilitySummary = ea.visibility_summary;
  const mustShow =
    endpoints.length > 0 ||
    sockets.length > 0 ||
    direct ||
    fullBypass ||
    declared.length > 0 ||
    detections.length > 0 ||
    visibilitySummary === "unobserved" ||
    visibilitySummary === "mixed" ||
    visibilitySummary === "unknown";

  return {
    summary: summarize(ea, endpoints, sockets, direct, fullBypass),
    visibilitySummary,
    visibilityAnnouncement: announceVisibility(visibilitySummary, {
      observed: endpoints.length > 0,
      declared: declared.length > 0,
      unobserved: direct || sockets.length > 0 || fullBypass,
    }),
    endpoints,
    sockets,
    declared,
    direct,
    fullBypass,
    detections,
    mustShow,
  };
}

function summarize(
  ea: ExternalAccess,
  endpoints: ExternalAccessEndpointRow[],
  sockets: ExternalAccessSocketRow[],
  direct: boolean,
  fullBypass: boolean,
): string {
  const parts: string[] = [];
  if (fullBypass) parts.push("full bypass");
  if (endpoints.length) {
    const denied = endpoints.filter((e) => e.decision === "deny").length;
    const base = `${endpoints.length} ${endpoints.length === 1 ? "endpoint" : "endpoints"}`;
    parts.push(denied > 0 ? `${base} · ${denied} denied` : base);
  }
  if (sockets.length) {
    parts.push(
      `${sockets.length} ${sockets.length === 1 ? "socket" : "sockets"} · authority available`,
    );
  }
  if (direct) parts.push("Destinations unobserved");
  if (ea.visibility_summary === "mixed" && !parts.includes("mixed")) {
    // mixed is implied by endpoints + socket/direct
  }
  if (parts.length === 0) {
    if (ea.visibility_summary === "unknown") return "visibility unknown";
    if (ea.visibility_summary === "unobserved") return "Destinations unobserved";
    if (ea.visibility_summary === "none") return "none";
    return ea.visibility_summary;
  }
  return parts.join(" · ");
}

function announceVisibility(
  summary: ExternalAccessVisibilitySummary,
  flags: { observed: boolean; declared: boolean; unobserved: boolean },
): string {
  const bits: string[] = [`Visibility summary: ${summary}.`];
  if (flags.observed) bits.push("Includes observed mediated endpoints.");
  if (flags.declared) bits.push("Includes declared destinations that were not observed.");
  if (flags.unobserved) {
    bits.push(
      "Includes unobserved authority: destinations or connections the host could not see.",
    );
  }
  if (summary === "unknown") {
    bits.push("Facts are incomplete; absence of endpoints is not proof of no access.");
  }
  return bits.join(" ");
}

function endpointRow(ep: ExternalAccessEndpoint): ExternalAccessEndpointRow {
  const host = ep.host.trim();
  const port = ep.port ?? 0;
  const transport = ep.transport ?? "";
  const hostPort =
    port > 0 ? formatHostPort(host, port) : host || "(redacted host)";
  const transportLabel = transportLabelFor(transport);
  const attempts = ep.attempt_count > 1 ? ` · ${ep.attempt_count} attempts` : "";
  const decision = ep.decision === "deny" ? "denied" : "allowed";
  return {
    host,
    port,
    transport,
    decision: ep.decision === "deny" ? "deny" : "allow",
    attemptCount: ep.attempt_count,
    label: `${hostPort} · ${transportLabel} · ${decision}${attempts}`,
    visibilityLabel: "observed mediated endpoint",
  };
}

function socketRow(s: ExternalAccessSocket): ExternalAccessSocketRow {
  const resolved = s.resolved_path?.trim() || s.approved_path?.trim() || "";
  const base = basenamePath(resolved);
  const pathLabel = base || resolved || "(redacted socket path)";
  return {
    pathLabel: resolved && resolved !== pathLabel ? `${pathLabel} (${shortPath(resolved)})` : pathLabel,
    scope: s.scope?.trim() || "current_action",
    authorityLabel: "authority available — outside-sandbox daemon; connection unobserved",
    visibilityLabel:
      "capability observed; connection and inner effects unobserved",
  };
}

function detectionRow(d: ExternalAccessDetection): ExternalAccessDetectionRow {
  const title = d.rule_title?.trim() || d.rule_id?.trim() || "detection";
  const citation = [d.pack_id, d.rule_id, d.action_id ? `action ${d.action_id}` : ""]
    .filter(Boolean)
    .join(" · ");
  return {
    title,
    level: d.level?.trim() || "",
    citation,
    incompleteNote:
      "Pattern-based detection (incomplete). Does not deny the capability itself.",
  };
}

function formatHostPort(host: string, port: number): string {
  if (host.includes(":") && !host.startsWith("[")) {
    return `[${host}]:${port}`;
  }
  if (host.startsWith("[")) return `${host}:${port}`;
  return `${host}:${port}`;
}

function transportLabelFor(transport: string): string {
  switch (transport) {
    case "socks_tcp":
      return "SOCKS TCP";
    case "http_request":
      return "HTTP";
    case "http_connect":
      return "HTTP CONNECT";
    default:
      return transport || "mediated";
  }
}

function basenamePath(path: string): string {
  const trimmed = path.replace(/\/+$/, "");
  const idx = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
}

function shortPath(path: string): string {
  if (path.length <= 48) return path;
  return `${path.slice(0, 20)}…${path.slice(-20)}`;
}
