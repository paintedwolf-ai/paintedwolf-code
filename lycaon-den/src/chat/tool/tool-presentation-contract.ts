import {
  COMPACTION_BANNER_CLOSE,
  COMPACTION_BANNER_OPEN,
  VERBATIM_HEAD_TAIL,
  OVERLAY_PROMOTE_EVENT_PREFIX,
  OVERLAY_REJECT_EVENT_PREFIX,
} from "../host-markers.generated.ts";
import { escapeRegExp } from "../../utils/escape-regexp.ts";
import { peelEvidenceHandleTag } from "./tool-output-handle.ts";
import { isRecord } from "../../utils/type-guards.ts";

export type ToolFact = {
  label: string;
  value: string;
  wide?: boolean;
  /** Host field path for the unchanged rendered value. */
  redactionField?: string;
  /** Source location carried by a tool argument or result. */
  path?: { path: string; line?: number; rootId?: string; entryKind?: "file" | "folder" };
  /** Host-data-relative spill location. */
  hostDataSpill?: boolean;
};

export type StructuredToolSection =
  | { kind: "facts"; facts: ToolFact[] }
  | {
      kind: "content";
      text: string;
      label?: string;
    }
  | { kind: "note"; text: string }
  | {
      kind: "compaction";
      banner: string;
      spillPath?: string;
    }
  | { kind: "log_digest"; view: import("./read-tool-output.ts").ReadLogDigestView }
  | {
      kind: "network";
      externalAccess: import("../../api/types.ts").ExternalAccess;
    }
  | {
      kind: "background_process";
      handle: string;
      running: boolean;
    };

export type StructuredToolPresentation = {
  argsFacts: ToolFact[];
  sections: StructuredToolSection[];
  evidenceHandle: string | null;
  /** Full output remains searchable without retaining its body here. */
  rawOutputAvailable: boolean;
  /** Hides raw output for protected tool results. */
  rawOutputProtected?: boolean;
  /** Guidance comes from host outcome, codes, or feedback. */
  guidance: boolean;
};

export function contentSection(
  text: string,
  options?: { label?: string },
): StructuredToolSection {
  return { kind: "content", text, label: options?.label };
}

/** Host compaction banners precede the result body. */
const TOOL_COMPACTION_BANNER_RE = new RegExp(
  `^${escapeRegExp(COMPACTION_BANNER_OPEN)}[^${escapeRegExp(COMPACTION_BANNER_CLOSE)}]+` +
    `${escapeRegExp(COMPACTION_BANNER_CLOSE)}\n?`,
);

type PeeledCompaction = {
  banner: string | null;
  body: string;
};

export function peelCompactionBanner(content: string): PeeledCompaction {
  const trimmedStart = content.replace(/^\s+/, "");
  const match = TOOL_COMPACTION_BANNER_RE.exec(trimmedStart);
  if (!match) {
    return { banner: null, body: content };
  }
  return {
    banner: match[0].trimEnd(),
    body: trimmedStart.slice(match[0].length),
  };
}

/** Removes host wrappers before payload parsing. */
export function normalizeToolWireOutput(content: string): {
  compactionBanner: string | null;
  evidenceHandle: string | null;
  body: string;
} {
  const leading = peelEvidenceHandleTag(content.trimStart());
  const { banner: compactionBanner, body: afterBanner } =
    peelCompactionBanner(leading.body);
  let rest = afterBanner;
  if (compactionBanner && !toolBodyAfterPrefix(rest).startsWith("{")) {
    const marker = `${VERBATIM_HEAD_TAIL}\n`;
    const trimmed = rest.trimStart();
    if (trimmed.startsWith(marker)) rest = trimmed.slice(marker.length);
    else {
      const markerIdx = trimmed.indexOf(`\n${marker}`);
      if (markerIdx >= 0) rest = trimmed.slice(markerIdx + marker.length + 1);
    }
  }
  const payload = compactionBanner ? peelEvidenceHandleTag(rest.trimStart()) : leading;
  return { compactionBanner, evidenceHandle: payload.handle ?? leading.handle, body: payload.body };
}

/** Removes host line prefixes before structured rendering. */
export function stripReadLineNumbers(text: string): string {
  const lines = text.split("\n");
  let numbered = 0;
  for (const line of lines) {
    if (/^\d+\t/.test(line)) numbered++;
  }
  if (numbered === 0) return text;
  if (numbered < Math.ceil(lines.length * 0.5)) return text;
  return lines.map((line) => line.replace(/^\d+\t/, "")).join("\n");
}

/** Extracts readable content from complete or compacted tool JSON. */
export function extractReadableToolBody(
  parsed: Record<string, unknown> | null,
  rawJson?: string,
): string | null {
  if (parsed) {
    for (const key of ["content", "text", "body", "stdout", "stderr", "tail", "output"] as const) {
      const value = parsed[key];
      if (typeof value === "string" && value.length) {
        return key === "content" || key === "text" || key === "body"
          ? stripReadLineNumbers(value)
          : value;
      }
    }
  }
  if (!rawJson) return null;
  return extractTruncatedJsonStringField(rawJson, "content");
}

/** Compaction can cut a JSON string before its closing quote. */
function extractTruncatedJsonStringField(
  raw: string,
  field: string,
): string | null {
  if (!raw.trimStart().startsWith("{")) return null;
  const key = `"${field}"`;
  const keyIdx = raw.indexOf(key);
  if (keyIdx < 0) return null;
  const colon = raw.indexOf(":", keyIdx + key.length);
  if (colon < 0) return null;
  let i = colon + 1;
  while (i < raw.length && (raw[i] === " " || raw[i] === "\t" || raw[i] === "\n")) {
    i++;
  }
  if (raw[i] !== '"') return null;
  i++;
  let out = "";
  let escape = false;
  for (; i < raw.length; i++) {
    const ch = raw[i]!;
    if (escape) {
      out += unescapeJsonChar(ch);
      escape = false;
      continue;
    }
    if (ch === "\\") {
      escape = true;
      continue;
    }
    if (ch === '"') break;
    out += ch;
  }
  return out.length ? stripReadLineNumbers(out) : null;
}

function unescapeJsonChar(ch: string): string {
  switch (ch) {
    case "n":
      return "\n";
    case "t":
      return "\t";
    case "r":
      return "\r";
    case '"':
      return '"';
    case "\\":
      return "\\";
    case "/":
      return "/";
    default:
      return ch;
  }
}

function toolBodyAfterPrefix(content: string): string {
  let body = peelEvidenceHandleTag(content.trim()).body.trimStart();
  for (const prefix of [OVERLAY_PROMOTE_EVENT_PREFIX, OVERLAY_REJECT_EVENT_PREFIX]) {
    if (body.startsWith(prefix)) {
      body = body.slice(prefix.length).trimStart();
      break;
    }
  }
  return body;
}

/** Parses a leading object after declared host and evidence prefixes. */
export function parseToolJsonObject(
  content: string,
): { parsed: Record<string, unknown>; jsonBody: string } | null {
  const body = toolBodyAfterPrefix(content);
  if (!body.startsWith("{")) return null;
  let depth = 0;
  let inString = false;
  let escaped = false;
  for (let i = 0; i < body.length; i++) {
    const ch = body[i];
    if (inString) {
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') inString = true;
    else if (ch === "{") depth++;
    else if (ch === "}" && --depth === 0) {
      const jsonBody = body.slice(0, i + 1);
      try {
        const parsed: unknown = JSON.parse(jsonBody);
        return isRecord(parsed) ? { parsed, jsonBody } : null;
      } catch {
        return null;
      }
    }
  }
  return null;
}

const UNSTRUCTURED_FALLBACK_NOTE =
  "Structured output — expand raw for the full payload.";

function isRawJsonBlob(text: string): boolean {
  const t = text.trim();
  return (
    (t.startsWith("{") && t.endsWith("}")) ||
    (t.startsWith("[") && t.endsWith("]"))
  );
}

/** Checks structured views for unreadable raw payloads. */
export function violatesStructuredToolPresentation(
  view: StructuredToolPresentation,
  output: string,
): string | null {
  const trimmed = peelEvidenceHandleTag(output).body.trim();
  if (!trimmed) return null;

  if (view.guidance) {
    if (view.rawOutputAvailable) {
      return "agent guidance must not duplicate raw disclosure";
    }
    if (!view.sections.some((section) => section.kind === "content")) {
      return "agent guidance must expose readable content";
    }
    return null;
  }

  if (!view.rawOutputAvailable && !view.rawOutputProtected) {
    return "non-guidance tool output must expose rawOutputAvailable";
  }
  if (view.rawOutputAvailable && view.rawOutputProtected) {
    return "protected output must not expose rawOutputAvailable";
  }

  for (const section of view.sections) {
    if (section.kind !== "content") continue;
    if (isRawJsonBlob(section.text) && section.label !== "Body") {
      return "primary content must not be a raw JSON blob";
    }
  }

  return null;
}

/** Checks structured views for missing readable presentation. */
export function readablePresentationIssues(
  view: StructuredToolPresentation,
  output: string,
): string[] {
  const issues: string[] = [];
  const violation = violatesStructuredToolPresentation(view, output);
  if (violation) issues.push(violation);

  const peeled = peelEvidenceHandleTag(output);
  const { banner, body } = peelCompactionBanner(peeled.body);
  const trimmed = body.trim();

  if (banner) {
    const surfaced = view.sections.some(
      (section) =>
        section.kind === "compaction" &&
        section.banner.includes(COMPACTION_BANNER_OPEN),
    );
    if (!surfaced) {
      issues.push("compacted wire must surface a compaction section");
    }
  }

  if (
    trimmed.includes(VERBATIM_HEAD_TAIL) ||
    (trimmed.includes('"content"') && trimmed.includes("\\n"))
  ) {
    const hasReadableContent = view.sections.some(
      (section) =>
        section.kind === "content" &&
        !section.text.trimStart().startsWith("{") &&
        !section.text.includes('"content"'),
    );
    if (!hasReadableContent && view.sections.some((s) => s.kind === "content")) {
      // Raw wire-shaped content is unreadable in the structured view.
      const rawLooking = view.sections.some(
        (section) =>
          section.kind === "content" &&
          (section.text.includes(VERBATIM_HEAD_TAIL) ||
            (section.text.includes('{"content"') && section.text.includes("\\n"))),
      );
      if (rawLooking) {
        issues.push("tool body must be parsed out of wire JSON / compaction residue");
      }
    }
  }

  if (trimmed && !view.guidance) {
    const hasRenderable =
      view.sections.some((section) => section.kind !== "note") ||
      view.argsFacts.length > 0 ||
      !!view.evidenceHandle;
    if (!hasRenderable) {
      issues.push("non-empty output must produce structured sections or input facts");
    }
  }

  if (
    view.sections.some(
      (section) =>
        section.kind === "note" && section.text === UNSTRUCTURED_FALLBACK_NOTE,
    )
  ) {
    issues.push("JSON payload fell through to unstructured fallback");
  }

  for (const section of view.sections) {
    if (section.kind !== "content") continue;
    if (/\\n/.test(section.text)) {
      issues.push("content must not contain escaped JSON newlines");
    }
    if (isRawJsonBlob(section.text) && section.label !== "Body") {
      issues.push("content must not be a raw JSON blob");
    }
    if (
      /Stream:\s*\w+/.test(section.text) &&
      /Cursor:\s*\d+/.test(section.text)
    ) {
      issues.push("stream chunks must coalesce into output text, not metadata lines");
    }
  }

  for (const section of view.sections) {
    if (section.kind !== "facts") continue;
    for (const fact of section.facts) {
      if (fact.value.includes("\n") && fact.value.length > 80) {
        issues.push(
          `fact "${fact.label}" must not hold multi-line body text — use a content section`,
        );
      }
    }
  }

  return issues;
}

/** Spill path keys the host may attach after wire compaction. */
export const WIRE_SPILL_PATH_KEYS = ["wire_spill_path", "spill_path"] as const;

export function spillPathFromJsonObject(
  obj: Record<string, unknown>,
): string | undefined {
  for (const key of WIRE_SPILL_PATH_KEYS) {
    const value = obj[key];
    if (typeof value === "string" && value.trim()) {
      return value.trim();
    }
  }
  return undefined;
}
