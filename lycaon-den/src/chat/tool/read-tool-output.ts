import type {
  LogDigest,
  LogDigestCluster,
  LogDigestFacet,
  OutlineKind,
} from "../../api/types.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";
import { isRecord } from "../../utils/type-guards.ts";

export type ReadLogDigestView = {
  path: string;
  outlineKind: OutlineKind;
  totalLines: number;
  digest: LogDigest;
  truncationBanner?: string;
};

function parseLogDigest(raw: unknown): LogDigest | null {
  if (!isRecord(raw)) return null;
  const format = raw.format;
  if (typeof format !== "string" || !format.trim()) return null;
  if (typeof raw.record_count !== "number" || typeof raw.parsed_count !== "number") {
    return null;
  }
  if (typeof raw.truncated !== "boolean") return null;

  const digest: LogDigest = {
    format: format as LogDigest["format"],
    record_count: raw.record_count,
    parsed_count: raw.parsed_count,
    truncated: raw.truncated,
  };

  if (isRecord(raw.time_span)) {
    const start = raw.time_span.start;
    const end = raw.time_span.end;
    if (typeof start === "string" && typeof end === "string") {
      digest.time_span = { start_at: start, end_at: end };
    }
  }

  if (Array.isArray(raw.fields)) {
    digest.fields = raw.fields.flatMap((item) => {
      if (!isRecord(item)) return [];
      if (typeof item.key !== "string" || typeof item.coverage_pct !== "number") {
        return [];
      }
      return [{ key: item.key, coverage_pct: item.coverage_pct }];
    });
  }

  if (Array.isArray(raw.facets)) {
    digest.facets = raw.facets.flatMap((item) => parseFacet(item));
  }

  if (Array.isArray(raw.clusters)) {
    digest.clusters = raw.clusters.flatMap((item) => parseCluster(item));
  }

  return digest;
}

function parseFacet(raw: unknown): LogDigestFacet[] {
  if (!isRecord(raw) || typeof raw.key !== "string" || !Array.isArray(raw.values)) {
    return [];
  }
  const values = raw.values.flatMap((entry) => {
    if (!isRecord(entry)) return [];
    if (typeof entry.value !== "string" || typeof entry.count !== "number") return [];
    return [{ value: entry.value, count: entry.count }];
  });
  if (!values.length) return [];
  return [{ key: raw.key, values }];
}

function parseCluster(raw: unknown): LogDigestCluster[] {
  if (!isRecord(raw)) return [];
  if (
    typeof raw.template !== "string" ||
    typeof raw.count !== "number" ||
    typeof raw.first_line !== "number" ||
    typeof raw.last_line !== "number"
  ) {
    return [];
  }
  const cluster: LogDigestCluster = {
    template: raw.template,
    count: raw.count,
    first_line: raw.first_line,
    last_line: raw.last_line,
  };
  if (typeof raw.severity === "string" && raw.severity.trim()) {
    cluster.severity = raw.severity;
  }
  return [cluster];
}

/** Parse native read JSON when outline_kind is log_digest. */
export function readLogDigestFromOutput(output: string): ReadLogDigestView | null {
  if (!output.trim().startsWith("{")) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(output) as unknown;
  } catch {
    return null;
  }
  if (!isRecord(parsed)) return null;
  if (parsed.outline_kind !== "log_digest") return null;
  const digest = parseLogDigest(parsed.log_digest);
  if (!digest) return null;
  const path = typeof parsed.path === "string" ? parsed.path : "";
  const totalLines =
    typeof parsed.total_lines === "number" ? parsed.total_lines : digest.record_count;
  const banner =
    typeof parsed.truncation_banner === "string"
      ? parsed.truncation_banner.trim()
      : undefined;
  return {
    path,
    outlineKind: "log_digest",
    totalLines,
    digest,
    truncationBanner: banner || undefined,
  };
}

export function formatLogFormatLabel(format: string): string {
  const known: Readonly<Record<string, string>> = {
    json_lines: "JSON lines",
    syslog_rfc5424: "Syslog RFC 5424",
    syslog_rfc3164: "Syslog RFC 3164",
    cef: "CEF",
    leef: "LEEF",
    clf: "CLF",
  };
  return known[format] ?? formatSentenceCase(format);
}

function formatLogTimestamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toISOString().replace("T", " ").replace(/\.\d{3}Z$/, " UTC");
}

export function formatTimeSpan(start: string, end: string): string {
  const s = formatLogTimestamp(start);
  const e = formatLogTimestamp(end);
  if (s === e) return s;
  return `${s} → ${e}`;
}
