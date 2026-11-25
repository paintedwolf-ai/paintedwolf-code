import type { BriefingRecordItem } from "../api/types.ts";
import { briefingCopy } from "./briefing-copy.ts";

const copy = briefingCopy.evidence;

/** One block of an input: an optional two-part caption, then the text. */
export type EvidenceInputPart = { head?: readonly [string, string]; text: string };

const PROSE_KEYS: ReadonlySet<string> = new Set([
  "user_intent", "worker_brief", "tracked_plan", "assistant_note", "assistant_reasoning", "phase_scope",
  "steps", "policy", "state", "effects", "assignment", "outcome", "report",
]);

/** Whether an input reads as sentences; everything else is shown literally. */
export function evidenceInputIsProse(key: string): boolean {
  return PROSE_KEYS.has(key);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function text(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value);
}

function recentResults(stored: string): EvidenceInputPart[] | undefined {
  const parsed: unknown = JSON.parse(stored);
  if (!Array.isArray(parsed) || !parsed.every(isRecord)) return undefined;
  return parsed.map((result) => ({ head: [text(result.tool ?? ""), text(result.outcome ?? "")], text: text(result.content ?? "") }));
}

const ACTION_PREFIX = "tool=";
const ACTION_ARGS = "\nargs=";

function action(stored: string): EvidenceInputPart[] | undefined {
  const split = stored.indexOf(ACTION_ARGS);
  if (!stored.startsWith(ACTION_PREFIX) || split < 0) return undefined;
  const args: unknown = JSON.parse(stored.slice(split + ACTION_ARGS.length));
  if (!isRecord(args)) return undefined;
  const { cwd, ...rest } = args;
  const names = Object.keys(rest);
  const commandOnly = names.length === 1 && typeof rest.command === "string";
  return [{
    head: [stored.slice(ACTION_PREFIX.length, split), typeof cwd === "string" ? copy.workingDirectory(cwd) : ""],
    text: commandOnly ? text(rest.command) : JSON.stringify(typeof cwd === "string" ? rest : args, null, 2),
  }];
}

function receipts(stored: string): EvidenceInputPart[] | undefined {
  const rows = stored.split("\n").filter((line) => line.trim()).map((line): unknown => JSON.parse(line));
  if (rows.length === 0 || !rows.every(isRecord)) return undefined;
  return rows.map((row) => ({
    head: [text(row.state ?? ""), typeof row.status_code === "number" ? copy.exitStatus(row.status_code) : ""],
    text: text(row.command ?? ""),
  }));
}

function indented(stored: string): EvidenceInputPart[] | undefined {
  const first = stored.trimStart()[0];
  if (first !== "{" && first !== "[") return undefined;
  return [{ text: JSON.stringify(JSON.parse(stored), null, 2) }];
}

const SHAPES: Readonly<Record<string, (stored: string) => EvidenceInputPart[] | undefined>> = {
  recent_results: recentResults, action, receipts,
};

/**
 * Lays out an input's stored text for reading. Text that is not in its key's
 * stored form, such as a cut one, is shown exactly as stored.
 */
export function evidenceInputParts(item: Pick<BriefingRecordItem, "key" | "text">): EvidenceInputPart[] {
  const stored = item.text ?? "";
  if (!stored) return [];
  try {
    return SHAPES[item.key]?.(stored) ?? indented(stored) ?? [{ text: stored }];
  } catch {
    return [{ text: stored }];
  }
}
