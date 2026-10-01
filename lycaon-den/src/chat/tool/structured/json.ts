import { workerTranscriptMarkdownSource } from "../../markdown/markdown-output.ts";
import { isRecord } from "../../../utils/type-guards.ts";
import { contentSection, extractReadableToolBody, stripReadLineNumbers, type StructuredToolSection, type ToolFact, WIRE_SPILL_PATH_KEYS } from "../tool-presentation-contract.ts";
import { PATH_FACT_KEYS, sourceFactPath, normalizeJsonKey, humanizeKey, formatScalar } from "./format.ts";
import { sectionFromArray, pushArraySections } from "./arrays.ts";
import { discoverySections } from "./discovery.ts";
import { LOCAL_AI_LABEL } from "../../turnload/turn-load-rows.ts";

const JSON_FACT_KEYS = [
  "message",
  "summary",
  "compact",
  "phase",
  "current_phase",
  "workflow_id",
  "workflow_version",
  "blueprint_path",
  "blueprint_title",
  "job_id",
  "status",
  "reason",
  "failed_gate",
  "code",
  "path",
  "file_path",
  "ok",
  "available",
  "mode",
  "outline_kind",
  "total_lines",
  "offset",
  "limit",
  "end_line",
  "start_line",
  "truncated",
  "exit_code",
  "stdin_provided",
  "stdin_from",
  "stdout_to",
  "stderr_to",
  "env_keys",
  "branch",
  "dirty",
  "hash",
  "count",
  "results_total",
  "results_truncated",
  "overlay_id",
  "leg_status",
  "resolution",
  "scope_stated",
  "query_stated",
  "removed",
  "next_action",
  "total_results",
  "next_offset",
  "max_depth",
  "max_results",
  "deeper_paths_omitted",
  "symbol",
  "kind",
  "language",
  "outline_source",
  "clamped",
  "total_entries",
  "path_a",
  "path_b",
  "context_lines",
  "staged_count",
  "unstaged_count",
  "format",
  "query",
  "ref",
  "insertions",
  "deletions",
  "conflict_tier",
  "parses",
  "task",
  "completeness",
  "selected",
  "total",
  "probes_run",
  "bundle",
  "view",
  "final_url",
  "duration_ms",
  "bytes",
  "sha256",
  "content_type",
  "body_encoding",
  "body_omitted",
  "body_spill_path",
  "body_unavailable",
  "response_path",
  "written",
  "cookies",
  "redacted",
  "redaction_receipt",
] as const;

const JSON_NOTE_KEYS = new Set([
  "note",
  "truncation_banner",
  "depth_notice",
  "error",
]);

const NESTED_RAW_KEYS = new Set([
  "board",
  "content",
  "text",
  "stdout",
  "stderr",
  "tail",
  "output",
  "body",
  "details",
  "diff",
  "failed_leaves",
  "log_digest",
  "digest",
  "tree",
  "shape",
  "overlay_intent",
]);

const JSON_ARRAY_KEYS = new Set([
  "results",
  "entries",
  "commits",
  "branches",
  "refs",
  "symbols",
  "matches",
  "files",
  "options",
  "ranges",
  "items",
  "paths",
  "candidates",
  "errors",
  "hunks",
  "restored",
  "lines",
  "recent_commits",
  "stages",
  "chunks",
  "values",
  "copies",
  "moves",
  "highlights",
  "distribution",
  "snapshot",
  "applied",
  "clean_paths",
  "context_before",
  "context_after",
  "identity",
  "skeleton",
  "substance",
  "anchors",
  "next_actions",
  "imports",
  "call_sites",
  "neighbors",
  "gaps",
  "sources_touched",
  "truncation",
  "headers",
  "redirects",
]);

const NESTED_CONTENT_STRING_KEYS = new Set([
  "content",
  "text",
  "body",
  "diff",
  "board",
  "overlay_intent",
  "stdout",
  "stderr",
  "tail",
  "output",
  "details",
  "digest",
  "tree",
  "shape",
]);

const NESTED_ABSORB_MAX_DEPTH = 2;

/** What the local engine ranked inside the call, from the result's `rerank` receipt. */
function rerankFact(rerank: Record<string, unknown>): ToolFact | undefined {
  const scored = typeof rerank.scored === "number" ? rerank.scored : 0;
  const candidates = typeof rerank.candidates === "number" ? rerank.candidates : 0;
  if (scored <= 0) return undefined;
  const elapsed = typeof rerank.elapsed_ms === "number" ? ` · ${rerank.elapsed_ms} ms` : "";
  const sites = Array.isArray(rerank.sites) ? rerank.sites.filter((s): s is string => typeof s === "string").length : 0;
  const where = sites > 1 ? ` across ${sites} lists` : "";
  return {
    label: LOCAL_AI_LABEL,
    value: `Ranked ${scored} of ${candidates} candidates${where}${elapsed}`,
    wide: true,
  };
}

function receiptFacts(receipt: Record<string, unknown>): ToolFact[] {
  const facts: ToolFact[] = [];
  const tool = receipt.tool;
  if (typeof tool === "string" && tool.trim()) {
    facts.push({ label: "Survey", value: tool.trim() });
  }
  const path = receipt.path;
  if (typeof path === "string" && path.trim()) {
    facts.push({
      label: "Path",
      value: path.trim(),
      path: sourceFactPath(path, receipt),
    });
  }
  const bytes = receipt.bytes_returned;
  if (typeof bytes === "number") {
    facts.push({ label: "Bytes", value: String(bytes) });
  }
  const pathsTouched = receipt.paths_touched;
  if (typeof pathsTouched === "number" && pathsTouched > 1) {
    facts.push({ label: "Paths touched", value: String(pathsTouched) });
  }
  if (receipt.truncated === true) {
    facts.push({ label: "Truncated", value: "Yes" });
  }
  return facts;
}

function pushNestedStringContent(
  sections: StructuredToolSection[],
  key: string,
  text: string,
): void {
  const normalized = normalizeJsonKey(key);
  const body = ["content", "text", "body"].includes(normalized)
    ? stripReadLineNumbers(text)
    : text;
  sections.push(contentSection(body, { label: humanizeKey(key) }));
}

function absorbNestedValue(
  key: string,
  value: unknown,
  facts: ToolFact[],
  content: StructuredToolSection[],
  depth: number,
): void {
  if (depth > NESTED_ABSORB_MAX_DEPTH) return;
  const label = humanizeKey(key);

  if (Array.isArray(value)) {
    pushArraySections(content, key, value);
    return;
  }
  if (!isRecord(value)) {
    facts.push({ label, value: formatScalar(value) });
    return;
  }

  for (const [childKey, childValue] of Object.entries(value)) {
    if (childValue === null || childValue === undefined) continue;
    const childLabel = normalizeJsonKey(key) === "tree" && childKey === "children"
      ? "Files and folders" : `${label} ${humanizeKey(childKey)}`;
    if (Array.isArray(childValue)) {
      pushArraySections(content, childLabel, childValue);
      continue;
    }
    if (isRecord(childValue)) {
      absorbNestedValue(childLabel, childValue, facts, content, depth + 1);
      continue;
    }
    if (
      typeof childValue === "string" &&
      childValue.trim() &&
      NESTED_CONTENT_STRING_KEYS.has(normalizeJsonKey(childKey))
    ) {
      pushNestedStringContent(content, childKey, childValue);
      continue;
    }
    facts.push({ label: childLabel, value: formatScalar(childValue) });
  }
}

function factsFromJsonObject(
  obj: Record<string, unknown>,
): {
  facts: ToolFact[];
  content: StructuredToolSection[];
} {
  const facts: ToolFact[] = [];
  const content: StructuredToolSection[] = [];
  const rest: Record<string, unknown> = { ...obj };

  // Top-level fields take precedence over packed fields.
  const pack = rest.pack;
  if (isRecord(pack)) {
    delete rest.pack;
    for (const [key, value] of Object.entries(pack)) {
      if (value === null || value === undefined) continue;
      if (rest[key] !== undefined) continue;
      rest[key] = value;
    }
  }

  for (const key of WIRE_SPILL_PATH_KEYS) {
    delete rest[key];
  }

  const omittedRange = rest.results_omitted_range;
  if (isRecord(omittedRange)) {
    const from = omittedRange.from;
    const to = omittedRange.to;
    if (typeof from === "number" && typeof to === "number") {
      facts.push({ label: "Omitted", value: `${from}–${to}` });
    }
    delete rest.results_omitted_range;
  }

  const receipt = rest.receipt;
  if (isRecord(receipt)) {
    facts.push(...receiptFacts(receipt));
    delete rest.receipt;
  }

  const rerank = rest.rerank;
  if (isRecord(rerank)) {
    delete rest.rerank;
    const fact = rerankFact(rerank);
    if (fact) facts.push(fact);
  }

  const network = rest.network;
  if (Array.isArray(network)) {
    delete rest.network;
  }

  const confined = rest.confined;
  if (typeof confined === "boolean") {
    delete rest.confined;
    const rawMode = rest.network_mode;
    delete rest.network_mode;
    const rawPosture = rest.network_posture;
    delete rest.network_posture;
    const parts = [rawMode, rawPosture].filter(
      (part): part is string => typeof part === "string" && part.length > 0,
    );
    facts.push({
      label: "Containment",
      value: confined ? ["on", ...parts].join(" · ") : "off — unconfined",
    });
  }

  const remotePackageExecution = rest.remote_package_execution;
  if (isRecord(remotePackageExecution)) {
    delete rest.remote_package_execution;
    const parts: string[] = [];
    if (remotePackageExecution.environment === "reduced") parts.push("reduced environment");
    if (remotePackageExecution.ambient_credentials_removed === true) {
      parts.push("no inherited credentials");
    }
    const approvedReads = Array.isArray(remotePackageExecution.approved_read_paths)
      ? remotePackageExecution.approved_read_paths.filter(
          (value): value is string => typeof value === "string" && value.trim().length > 0,
        )
      : [];
    if (approvedReads.length > 0) {
      parts.push(`approved read access to ${approvedReads.join(", ")}`);
    }
    if (remotePackageExecution.protected_reads_denied === true) {
      parts.push(approvedReads.length > 0 ? "other protected reads require approval" : "protected reads require approval");
    }
    const allowedHosts = Array.isArray(remotePackageExecution.allowed_hosts)
      ? remotePackageExecution.allowed_hosts.filter(
          (value): value is string => typeof value === "string" && value.trim().length > 0,
        )
      : [];
    if (remotePackageExecution.network_scope === "registry_only") {
      parts.push(
        allowedHosts.length > 0
          ? `network only to ${allowedHosts.join(", ")}`
          : "registry-only network",
      );
    }
    facts.push({
      label: "Downloaded code",
      value: parts.length > 0 ? parts.join(" · ") : "restricted package execution",
    });
  }

  // A non-matching pass is unverified.
  const confirmed = rest.confirmed;
  if (typeof confirmed === "boolean") {
    delete rest.confirmed;
    facts.push({
      label: "Verification",
      value: confirmed ? "host-confirmed" : "not host-confirmed",
    });
  }

  const selected = rest.selected;
  const total = rest.total;
  if (typeof selected === "number" && typeof total === "number") {
    delete rest.selected;
    delete rest.total;
    facts.push({ label: "Coverage", value: `${selected} of ${total}` });
  }

  for (const key of JSON_FACT_KEYS) {
    const value = rest[key];
    if (value === undefined) continue;
    delete rest[key];
    if (NESTED_RAW_KEYS.has(key) || JSON_NOTE_KEYS.has(key)) continue;
    // Cookie receipts expose names and counts.
    if (key === "cookies" && isRecord(value) && typeof value.jar === "string") {
      const sent = typeof value.sent === "number" ? value.sent : 0;
      const stored = typeof value.stored === "number" ? value.stored : 0;
      const parts = [`${value.jar}`, `${sent} sent`, `${stored} stored`];
      if (value.persisted === false) parts.push("not persisted");
      facts.push({ label: "Cookie jar", value: parts.join(" · ") });
      const names = Array.isArray(value.names)
        ? value.names.filter((name): name is string => typeof name === "string")
        : [];
      if (names.length > 0) {
        facts.push({ label: "Cookies held", value: names.join(", ") });
      }
      continue;
    }
    if (key === "ok" && typeof value === "boolean") {
      facts.push({ label: "Result", value: value ? "OK" : "Failed" });
      continue;
    }
    if (typeof value === "string" && value.includes("\n")) {
      content.push(contentSection(value.trim()));
      continue;
    }
    const wide = key === "task" || key === "message" || key === "summary";
    const displayValue =
      key === "duration_ms" && typeof value === "number"
        ? `${value} ms`
        : key === "bytes" && typeof value === "number"
          ? `${value} B`
          : key === "redaction_receipt" &&
              typeof value === "string" &&
              value.trim()
            ? "Issued"
            : formatScalar(value);
    const fact: ToolFact = {
      label: humanizeKey(key),
      value: displayValue,
      wide,
    };
    if (
      PATH_FACT_KEYS.has(key) &&
      typeof value === "string" &&
      value.trim() &&
      !value.includes("\n")
    ) {
      fact.path = sourceFactPath(value, obj);
    }
    facts.push(fact);
  }

  for (const key of JSON_NOTE_KEYS) {
    const value = rest[key];
    if (typeof value !== "string" || !value.trim()) continue;
    delete rest[key];
    content.push({ kind: "note", text: value.trim() });
  }

  for (const key of NESTED_RAW_KEYS) {
    const value = rest[key];
    if (typeof value !== "string" || !value.trim()) continue;
    delete rest[key];
    pushNestedStringContent(content, key, value);
  }

  for (const key of JSON_ARRAY_KEYS) {
    const value = rest[key];
    if (!Array.isArray(value)) continue;
    delete rest[key];
    pushArraySections(content, key, value);
  }

  for (const [key, value] of Object.entries(rest)) {
    if (value === null || value === undefined) continue;
    if (Array.isArray(value)) {
      pushArraySections(content, key, value);
      delete rest[key];
      continue;
    }
    if (isRecord(value)) {
      absorbNestedValue(key, value, facts, content, 0);
      delete rest[key];
      continue;
    }
    if (typeof value === "string" && value.includes("\n")) {
      content.push(contentSection(value.trim()));
      delete rest[key];
      continue;
    }
    facts.push({ label: humanizeKey(key), value: formatScalar(value) });
    delete rest[key];
  }

  return { facts, content };
}

export function presentationFromJsonObject(
  parsed: Record<string, unknown>,
  raw: string,
): StructuredToolSection[] {
  const { facts, content } = factsFromJsonObject(parsed);
  const sections: StructuredToolSection[] = [];
  if (facts.length) sections.push({ kind: "facts", facts });
  sections.push(...content);
  const structured = facts.length > 0 || content.length > 0;
  if (!structured) {
    const markdown = workerTranscriptMarkdownSource(raw);
    if (markdown && markdown !== raw.trim()) {
      sections.push(contentSection(markdown));
      return sections;
    }
    sections.push({
      kind: "note",
      text: "Structured output — expand raw for the full payload.",
    });
  }
  return sections;
}

export function presentationFromJsonArray(parsed: unknown[]): StructuredToolSection[] {
  const section = sectionFromArray("items", parsed);
  return Array.isArray(section) ? section : [section];
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === "string") : [];
}

/** Discovery tools expose selections and catalogs as separate results. */
export function presentationFromDiscoveryToolJson(parsed: Record<string, unknown>): StructuredToolSection[] {
  const facts: ToolFact[] = [];
  const loaded = stringList(parsed.loaded);
  const already = stringList(parsed.already_loaded);
  if (loaded.length) facts.push({ label: "Loaded", value: loaded.join(", "), wide: true });
  if (already.length) facts.push({ label: "Already loaded", value: already.join(", "), wide: true });
  const discovery = discoverySections(parsed);
  const sections: StructuredToolSection[] = facts.length
    ? [{ kind: "facts", facts }]
    : discovery.length ? [] : [{ kind: "note", text: "Nothing loaded." }];
  sections.push(...discovery);
  const note = typeof parsed.note === "string" ? parsed.note.trim() : "";
  if (note) sections.push({ kind: "note", text: note });
  return sections;
}

export function presentationFromReadableFileJson(
  parsed: Record<string, unknown>,
  jsonBody: string,
): StructuredToolSection[] {
  const body = extractReadableToolBody(parsed, jsonBody);
  const rest = { ...parsed };
  for (const key of ["content", "text", "body"] as const) {
    delete rest[key];
  }
  const { facts, content } = factsFromJsonObject(rest);
  const sections: StructuredToolSection[] = [];
  if (facts.length) sections.push({ kind: "facts", facts });
  sections.push(...content);
  if (body !== null) {
    sections.push(contentSection(body));
  } else if (!sections.length) {
    sections.push({
      kind: "note",
      text: "Structured output — expand raw for the full payload.",
    });
  }
  return sections;
}
