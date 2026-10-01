import { type ToolFact } from "../tool-presentation-contract.ts";
import { formatSentenceCase } from "../../../format/format-sentence-case.ts";

const ARG_LABELS: Record<string, string> = {
  path: "Path",
  file: "File",
  file_path: "Path",
  command: "Command",
  pipeline: "Pipeline",
  stdin_from: "Stdin from",
  stdout_to: "Stdout to",
  stderr_to: "Stderr to",
  append: "Append",
  background: "Background",
  pattern: "Pattern",
  query: "Query",
  workflow_id: "Workflow",
  workflow_version: "Version",
  phase_id: "Phase",
  blueprint_path: "Plan",
  blueprint_title: "Blueprint title",
  subagent_type: "Agent",
  description: "Description",
  prompt: "Prompt",
  reason: "Reason",
  resolution: "Result",
  scope_stated: "Searched",
  query_stated: "Query read as",
  next_action: "Next",
  agent_type: "Agent",
  observed_at: "Observed",
  currency: "File since",
  removed: "Removed records",
  url: "URL",
  final_url: "Final URL",
  method: "Method",
  headers: "Headers",
  body_path: "Body file",
  timeout_ms: "Timeout",
  redirects: "Redirects",
  response_body: "Response body",
  duration_ms: "Duration",
  sha256: "SHA-256",
  content_type: "Content type",
  body_encoding: "Body encoding",
  body_omitted: "Body omitted",
  body_spill_path: "Body file",
  body_unavailable: "Body unavailable",
  response_path: "Response file",
  written: "Written",
  cookies: "Cookies",
  redaction_receipt: "Redaction receipt",
  dest: "Destination",
  handle: "Handle",
};

// These fields carry source paths in tool arguments and results.
export const PATH_FACT_KEYS = new Set([
  "path",
  "file",
  "file_path",
  "blueprint_path",
  "path_a",
  "path_b",
  "response_path",
  "dest",
]);

export function sourceFactPath(path: string, record: Record<string, unknown>): NonNullable<ToolFact["path"]> {
  return {
    path: path.trim(),
    ...(typeof record.root_id === "string" && record.root_id.trim() ? { rootId: record.root_id.trim() } : {}),
    ...(typeof record.is_dir === "boolean" ? { entryKind: record.is_dir ? "folder" as const : "file" as const } : {}),
  };
}

export function normalizeJsonKey(key: string): string {
  return key
    .replace(/([a-z0-9])([A-Z])/g, "$1_$2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1_$2")
    .toLowerCase();
}

export function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

export function humanizeKey(key: string): string {
  const direct = ARG_LABELS[key] ?? ARG_LABELS[normalizeJsonKey(key)];
  if (direct) return direct;
  return formatSentenceCase(normalizeJsonKey(key));
}

export function formatScalar(value: unknown): string {
  if (value === null || value === undefined) return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (typeof value === "number") return String(value);
  if (typeof value === "string") return value.trim() || "—";
  if (Array.isArray(value)) {
    const parts = value
      .map((v) => (typeof v === "string" ? v.trim() : formatScalar(v)))
      .filter((s) => s && s !== "—");
    return parts.length ? parts.join(", ") : "—";
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}
