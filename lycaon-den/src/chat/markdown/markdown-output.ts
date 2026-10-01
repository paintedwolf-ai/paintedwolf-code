import { peelFinalEnvelope } from "./harmony.ts";
import {
  WORKER_LEG_STATUSES,
  type WorkerLegStatus,
} from "../host-markers.generated.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

// Inline code may contain triple backticks that are not fence openers.
export function normalizeMarkdownFences(text: string): string {
  return text.replace(/([^\s`])[ \t]*```(\w*)(?=\r?\n)/g, "$1\n\n```$2");
}

/** Normalize streamed Markdown before parsing. */
export function prepareMarkdownSource(text: string): string {
  let out = normalizeMarkdownFences(text);
  // Insert a missing heading space.
  out = out.replace(/^(#{1,6})([^\s#])/gm, "$1 $2");
  // Separate prose from a following heading.
  out = out.replace(/([^\n])\n(#{1,6} +)/g, "$1\n\n$2");
  return out;
}

function decodeXmlText(raw: string): string {
  return raw
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&")
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'");
}

function tagText(xml: string, tag: string): string {
  const re = new RegExp(
    `<${tag}(?:\\s[^>]*)?>([\\s\\S]*?)<\\/${tag}>`,
    "i",
  );
  const match = re.exec(xml);
  return match?.[1] ? decodeXmlText(match[1].trim()) : "";
}

/** Extract display prose from worker completion envelopes. */
function workerCompletionMarkdownBody(content: string): string | null {
  const trimmed = content.trim();
  if (!trimmed.startsWith("<")) return null;
  const taskMatch = /<task\b[^>]*>([\s\S]*?)<\/task>/i.exec(trimmed);
  if (!taskMatch) return null;
  const inner = taskMatch[1] ?? "";
  const summary = tagText(inner, "summary");
  const body = tagText(inner, "task_result") || summary;
  return body || null;
}

type WorkerCompletionReport = {
  leg_status: WorkerLegStatus;
  files_modified: string[];
  objectives_met: string[];
  remaining_risk: string[];
  suggested_next_task: string;
  brief: string;
};

const JSON_FENCE_RE = /```(?:json)?\s*([\s\S]*?)```/i;

// Cards accept only canonical leg statuses.
function normalizeWorkerReportLegStatus(status: string): WorkerLegStatus | "" {
  const candidate = status.trim().toLowerCase();
  return WORKER_LEG_STATUSES.find((known) => known === candidate) ?? "";
}

function normalizeReportStringList(items: unknown, max = 12): string[] {
  if (!Array.isArray(items)) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const item of items) {
    if (typeof item !== "string") continue;
    const text = item.trim();
    if (!text || seen.has(text)) continue;
    seen.add(text);
    out.push(text);
    if (max > 0 && out.length >= max) break;
  }
  out.sort();
  return out;
}

function decodeWorkerCompletionReport(
  candidate: string,
): WorkerCompletionReport | null {
  try {
    const parsed = JSON.parse(candidate) as Record<string, unknown>;
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return null;
    }
    const legStatus = normalizeWorkerReportLegStatus(
      typeof parsed.leg_status === "string" ? parsed.leg_status : "",
    );
    if (!legStatus) return null;
    return {
      leg_status: legStatus,
      files_modified: normalizeReportStringList(parsed.files_modified),
      objectives_met: normalizeReportStringList(parsed.objectives_met),
      remaining_risk: normalizeReportStringList(parsed.remaining_risk),
      suggested_next_task:
        typeof parsed.suggested_next_task === "string"
          ? parsed.suggested_next_task.trim()
          : "",
      brief: typeof parsed.brief === "string" ? parsed.brief.trim() : "",
    };
  } catch {
    return null;
  }
}

/** Envelope-only: bare JSON or a single fence with nothing outside. */
function envelopeOnlyWorkerReport(
  content: string,
): WorkerCompletionReport | null {
  const trimmed = content.trim();
  if (!trimmed) return null;
  const direct = decodeWorkerCompletionReport(trimmed);
  if (direct) return direct;
  const match = JSON_FENCE_RE.exec(trimmed);
  if (!match?.[1]) return null;
  if (trimmed.replace(new RegExp(JSON_FENCE_RE.source, "gi"), "").trim() !== "") {
    return null;
  }
  return decodeWorkerCompletionReport(match[1].trim());
}

function markdownBulletSection(title: string, items: string[]): string {
  if (!items.length) return "";
  const lines = items.map((item) => `- ${item}`);
  return `### ${title}\n\n${lines.join("\n")}`;
}

/** Worker finish JSON as markdown. */
function workerCompletionReportMarkdown(content: string): string | null {
  const report = envelopeOnlyWorkerReport(peelFinalEnvelope(content));
  if (!report) return null;
  const parts: string[] = [`## ${formatSentenceCase(report.leg_status)}`];
  if (report.brief) parts.push(report.brief);
  const objectives = markdownBulletSection(
    "Objectives met",
    report.objectives_met,
  );
  if (objectives) parts.push(objectives);
  const files = markdownBulletSection(
    "Files modified",
    report.files_modified,
  );
  if (files) parts.push(files);
  const risks = markdownBulletSection(
    "Remaining risk",
    report.remaining_risk,
  );
  if (risks) parts.push(risks);
  if (report.suggested_next_task) {
    parts.push(`### Suggested next\n\n${report.suggested_next_task}`);
  }
  return parts.join("\n\n");
}

/** Completion envelopes supply display prose for worker summaries. */
export function workerTranscriptMarkdownSource(content: string): string {
  const trimmed = content.trim();
  if (!trimmed) return "";
  const envelope = workerCompletionMarkdownBody(trimmed);
  if (envelope) return envelope;
  const report = workerCompletionReportMarkdown(trimmed);
  if (report) return report;
  return trimmed;
}
