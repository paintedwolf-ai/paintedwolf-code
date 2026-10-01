import { isRecord } from "../../../utils/type-guards.ts";
import { contentSection, type StructuredToolSection } from "../tool-presentation-contract.ts";
import { normalizeJsonKey, humanizeKey, formatScalar } from "./format.ts";

const ARRAY_PREVIEW_LIMIT = 40;

/** Renders record fields as readable lines. */
function formatRecordItem(item: Record<string, unknown>): string {
  const entries = Object.entries(item).filter(
    ([, v]) => v !== null && v !== undefined,
  );
  if (!entries.length) return "—";
  const allScalar = entries.every(([, v]) => typeof v !== "object");
  if (allScalar && entries.length <= 8) {
    return entries
      .map(([k, v]) => `${humanizeKey(k)}: ${formatScalar(v)}`)
      .join(" · ");
  }
  const lines: string[] = [];
  for (const [k, v] of entries) {
    if (typeof v === "object") {
      const nested = formatArrayItem(v);
      lines.push(
        nested.includes("\n")
          ? `${humanizeKey(k)}:\n${nested
              .split("\n")
              .map((line) => `  ${line}`)
              .join("\n")}`
          : `${humanizeKey(k)}: ${nested}`,
      );
      continue;
    }
    lines.push(`${humanizeKey(k)}: ${formatScalar(v)}`);
  }
  return lines.join("\n");
}

// Prefix changed observations with their observer and movement.
function formatRecalledObservation(
  item: Record<string, unknown>,
): string | undefined {
  const str = (key: string): string =>
    typeof item[key] === "string" ? (item[key] as string).trim() : "";
  const observedAt = str("observed_at");
  // hit_id identifies observation records without appearing in their labels.
  if (!observedAt || !str("hit_id")) return undefined;

  const handle = str("handle");
  const path = str("path");
  const line = typeof item.line === "number" && item.line > 0 ? item.line : 0;
  const currency = str("currency");
  const body = Array.isArray(item.body)
    ? item.body.filter((l): l is string => typeof l === "string").join("\n")
    : "";

  const lead = [
    str("agent_type") || (item.mine === true ? "this chat" : ""),
    handle || str("kind"),
    line ? `${path}:${line}` : path,
    currency && currency !== "unchanged" ? currency : "",
    item.untrusted === true ? "untrusted" : "",
  ].filter(Boolean);

  const excerpt = body || str("snippet");
  const head = lead.join(" · ") || observedAt;
  return excerpt ? `${head}\n${excerpt}` : head;
}

function formatArrayItem(item: unknown): string {
  if (typeof item === "string") {
    const trimmed = item.trim();
    if (!trimmed) return "—";
    if (
      (trimmed.startsWith("{") && trimmed.endsWith("}")) ||
      (trimmed.startsWith("[") && trimmed.endsWith("]"))
    ) {
      try {
        const parsed = JSON.parse(trimmed) as unknown;
        if (Array.isArray(parsed)) {
          return parsed.map(formatArrayItem).join("\n") || "—";
        }
        if (isRecord(parsed)) return formatRecordItem(parsed);
      } catch {
        // Malformed JSON remains readable as text.
      }
    }
    return trimmed;
  }
  if (typeof item === "number" || typeof item === "boolean") {
    return formatScalar(item);
  }
  if (!isRecord(item)) return formatScalar(item);

  const recalled = formatRecalledObservation(item);
  if (recalled) return recalled;

  const command = typeof item.command === "string" ? item.command : "";
  const exitCode = typeof item.exit_code === "number" ? item.exit_code : undefined;
  // A skipped stage has no exit code.
  const skipped = item.skipped === true;
  if (command && skipped) {
    return `${command} → skipped`;
  }
  if (command && exitCode !== undefined) {
    return `${command} → exit ${exitCode}`;
  }

  if (typeof item.from === "string" && typeof item.to === "string") {
    const bytes = typeof item.bytes === "number" ? ` (${item.bytes} B)` : "";
    return `${item.from} → ${item.to}${bytes}`;
  }

  if (typeof item.name === "string" && typeof item.line === "number") {
    const kind = typeof item.kind === "string" ? item.kind : "";
    const pathPrefix =
      typeof item.path === "string" && item.path.trim()
        ? `${item.path} `
        : "";
    return kind
      ? `${pathPrefix}L${item.line} ${kind} ${item.name}`.trim()
      : `${pathPrefix}L${item.line} ${item.name}`.trim();
  }

  if (typeof item.path === "string" && typeof item.line === "number") {
    const lineContent =
      typeof item.content === "string"
        ? item.content
        : typeof item.excerpt === "string"
          ? item.excerpt
          : typeof item.body === "string"
            ? item.body
            : "";
    const handle =
      typeof item.handle === "string" && item.handle.trim()
        ? `${item.handle} `
        : "";
    const ctxBefore = Array.isArray(item.context_before)
      ? item.context_before.map(formatArrayItem).join("\n")
      : "";
    const ctxAfter = Array.isArray(item.context_after)
      ? item.context_after.map(formatArrayItem).join("\n")
      : "";
    const main = `${handle}${item.path}:${item.line}: ${lineContent}`.trimEnd();
    return [ctxBefore, main, ctxAfter].filter(Boolean).join("\n");
  }

  if (
    typeof item.tool === "string" &&
    typeof item.why === "string" &&
    item.why.trim()
  ) {
    const path = typeof item.path === "string" ? item.path : "";
    const lines = typeof item.lines === "string" ? item.lines : "";
    const pattern = typeof item.pattern === "string" ? item.pattern : "";
    const target = [path, lines, pattern].filter(Boolean).join(" ");
    return target
      ? `${item.tool} ${target} — ${item.why.trim()}`
      : `${item.tool} — ${item.why.trim()}`;
  }

  if (
    typeof item.path === "string" &&
    typeof item.start_line === "number" &&
    typeof item.body === "string"
  ) {
    const end =
      typeof item.end_line === "number" ? item.end_line : item.start_line;
    const symbol =
      typeof item.symbol === "string" && item.symbol.trim()
        ? ` ${item.symbol}`
        : "";
    return `--- ${item.path}:${item.start_line}–${end}${symbol} ---\n${item.body.trim()}`;
  }

  if (typeof item.path === "string" && typeof item.status === "string") {
    return `${item.path} [${item.status}]`;
  }
  if (
    typeof item.path === "string" &&
    typeof item.insertions === "number" &&
    typeof item.deletions === "number"
  ) {
    return `${item.path} +${item.insertions} -${item.deletions}`;
  }
  if (typeof item.path === "string" && typeof item.why === "string" && item.why.trim()) {
    return `${item.path} — ${item.why.trim()}`;
  }
  if (typeof item.path === "string" && typeof item.type === "string") {
    const mode = typeof item.mode === "string" ? ` ${item.mode}` : "";
    const size = typeof item.size === "number" ? ` ${item.size} B` : "";
    return `${item.path} (${item.type}${mode}${size})`;
  }
  if (typeof item.path === "string" && typeof item.lines === "number") {
    const words =
      typeof item.words === "number" ? `, ${item.words} words` : "";
    const bytes = typeof item.bytes === "number" ? `, ${item.bytes} bytes` : "";
    return `${item.path}: ${item.lines} lines${bytes}${words}`;
  }
  if (typeof item.path === "string") {
    const extras: string[] = [];
    if (typeof item.mode === "string") extras.push(item.mode);
    if (typeof item.size === "number") extras.push(`${item.size} B`);
    if (typeof item.is_dir === "boolean") extras.push(item.is_dir ? "dir" : "file");
    if (typeof item.mtime === "string" && item.mtime.trim()) extras.push(item.mtime);
    if (typeof item.kind === "string" && item.kind.trim()) extras.push(item.kind);
    if (typeof item.line_count === "number") {
      extras.push(`${item.line_count} lines`);
    }
    if (typeof item.parse_health === "string" && item.parse_health.trim()) {
      extras.push(item.parse_health.trim());
    }
    return extras.length ? `${item.path} · ${extras.join(" · ")}` : item.path;
  }

  if (typeof item.name === "string" && typeof item.type === "string") {
    const mode = typeof item.mode === "string" ? ` ${item.mode}` : "";
    const size = typeof item.size === "number" ? ` ${item.size} B` : "";
    return `${item.name} (${item.type}${mode}${size})`;
  }

  if (typeof item.title === "string" && typeof item.url === "string") {
    const snippet =
      typeof item.snippet === "string" && item.snippet.trim()
        ? item.snippet.trim()
        : typeof item.description === "string" && item.description.trim()
          ? item.description.trim()
          : "";
    return snippet
      ? `${item.title} — ${item.url}\n  ${snippet}`
      : `${item.title} — ${item.url}`;
  }

  if (typeof item.hash === "string" || typeof item.subject === "string") {
    const hash =
      typeof item.hash === "string" ? item.hash.slice(0, 12) : "";
    const subject = typeof item.subject === "string" ? item.subject : "";
    return [hash, subject].filter(Boolean).join(" ");
  }
  if (typeof item.ref === "string" && typeof item.sha === "string") {
    const peeled =
      typeof item.peeled === "string" && item.peeled.trim()
        ? ` (${item.peeled.slice(0, 12)})`
        : "";
    return `${item.ref} → ${item.sha.slice(0, 12)}${peeled}`;
  }
  if (typeof item.name === "string" && typeof item.current === "boolean") {
    return item.current ? `* ${item.name}` : item.name;
  }
  if (typeof item.line === "number" && typeof item.commit === "string") {
    const content = typeof item.content === "string" ? item.content : "";
    return `L${item.line} ${item.commit.slice(0, 8)} ${content}`;
  }

  return formatRecordItem(item);
}

function isStreamChunkItem(item: unknown): boolean {
  return isRecord(item) && typeof item.text === "string";
}

function sectionFromStreamChunks(items: unknown[]): StructuredToolSection {
  const stdout: string[] = [];
  const stderr: string[] = [];
  const other: string[] = [];
  for (const item of items.slice(0, ARRAY_PREVIEW_LIMIT)) {
    if (!isRecord(item) || typeof item.text !== "string") continue;
    const stream = typeof item.stream === "string" ? item.stream : "stdout";
    if (stream === "stderr") stderr.push(item.text);
    else if (stream === "stdout") stdout.push(item.text);
    else other.push(item.text);
  }
  const parts: string[] = [];
  if (stdout.length) parts.push(stdout.join(""));
  if (stderr.length) {
    parts.push(
      parts.length ? `--- stderr ---\n${stderr.join("")}` : stderr.join(""),
    );
  }
  if (other.length) parts.push(other.join(""));
  let text = parts.join("\n") || "—";
  if (items.length > ARRAY_PREVIEW_LIMIT) {
    text += `\n… ${items.length - ARRAY_PREVIEW_LIMIT} more chunks`;
  }
  return contentSection(text, { label: "Output" });
}

export function sectionFromArray(
  key: string,
  items: unknown[],
): StructuredToolSection | StructuredToolSection[] {
  if (normalizeJsonKey(key) === "ranges") {
    return sectionFromReadRanges(items);
  }
  if (normalizeJsonKey(key) === "substance") {
    return sectionFromSubstanceWindows(items);
  }
  if (!items.length) {
    return { kind: "note", text: `${humanizeKey(key)}: none` };
  }
  if (
    (normalizeJsonKey(key) === "chunks" ||
      items.every(isStreamChunkItem)) &&
    items.some(isStreamChunkItem)
  ) {
    return sectionFromStreamChunks(items);
  }
  const lines = items.slice(0, ARRAY_PREVIEW_LIMIT).map(formatArrayItem);
  let text = lines.join("\n");
  if (items.length > ARRAY_PREVIEW_LIMIT) {
    text += `\n… ${items.length - ARRAY_PREVIEW_LIMIT} more`;
  }
  return contentSection(text, { label: humanizeKey(key) });
}

export function pushArraySections(
  target: StructuredToolSection[],
  key: string,
  items: unknown[],
): void {
  const section = sectionFromArray(key, items);
  if (Array.isArray(section)) target.push(...section);
  else target.push(section);
}

function sectionFromReadRanges(
  blocks: unknown[],
): StructuredToolSection[] {
  if (!blocks.length) {
    return [{ kind: "note", text: "Ranges: none" }];
  }
  const sections: StructuredToolSection[] = [];
  for (const block of blocks.slice(0, ARRAY_PREVIEW_LIMIT)) {
    if (!isRecord(block)) continue;
    const offset = typeof block.offset === "number" ? block.offset : "?";
    const endLine =
      typeof block.end_line === "number" ? block.end_line : offset;
    const text = typeof block.content === "string" ? block.content : "";
    if (!text.trim()) continue;
    sections.push(
      contentSection(text, {
        label: `lines ${offset}–${endLine}`,
      }),
    );
  }
  if (blocks.length > ARRAY_PREVIEW_LIMIT) {
    sections.push({
      kind: "note",
      text: `… ${blocks.length - ARRAY_PREVIEW_LIMIT} more ranges`,
    });
  }
  return sections.length
    ? sections
    : [{ kind: "note", text: "Ranges: none" }];
}

/** Each excerpt becomes one labeled body panel. */

function sectionFromSubstanceWindows(
  blocks: unknown[],
): StructuredToolSection[] {
  if (!blocks.length) {
    return [{ kind: "note", text: "Substance: none" }];
  }
  const sections: StructuredToolSection[] = [];
  for (const block of blocks.slice(0, ARRAY_PREVIEW_LIMIT)) {
    if (!isRecord(block)) continue;
    const path = typeof block.path === "string" ? block.path : undefined;
    const start =
      typeof block.start_line === "number" ? block.start_line : undefined;
    const end =
      typeof block.end_line === "number"
        ? block.end_line
        : start;
    const symbol =
      typeof block.symbol === "string" && block.symbol.trim()
        ? ` ${block.symbol.trim()}`
        : "";
    const body = typeof block.body === "string" ? block.body.trim() : "";
    if (!body) continue;
    const range =
      start !== undefined
        ? `${path ?? "—"}:${start}–${end ?? start}${symbol}`
        : path
          ? `${path}${symbol}`
          : "Substance";
    sections.push(contentSection(body, { label: range }));
  }
  if (blocks.length > ARRAY_PREVIEW_LIMIT) {
    sections.push({
      kind: "note",
      text: `… ${blocks.length - ARRAY_PREVIEW_LIMIT} more windows`,
    });
  }
  return sections.length
    ? sections
    : [{ kind: "note", text: "Substance: none" }];
}
