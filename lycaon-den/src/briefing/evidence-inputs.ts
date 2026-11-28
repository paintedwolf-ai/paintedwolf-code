import type { BriefingRecordItem } from "../api/types.ts";
import { briefingCopy } from "./briefing-copy.ts";

const copy = briefingCopy.evidence;

/** One block of an input: an optional two-part caption, the text as stored, and the language Pretty reads it as. */
type EvidenceInputPart = { head?: readonly [string, string]; text: string; language?: string };

function holdsJson(stored: string): boolean {
  const first = stored.trimStart()[0];
  if (first !== "{" && first !== "[") return false;
  JSON.parse(stored);
  return true;
}

function block(stored: string, head?: readonly [string, string]): EvidenceInputPart {
  let language: string | undefined;
  try { language = holdsJson(stored) ? "json" : undefined; } catch { language = undefined; }
  return { ...(head ? { head } : {}), text: stored, ...(language ? { language } : {}) };
}

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
  if (!Array.isArray(parsed) || !parsed.every((result) => isRecord(result) && typeof result.tool === "string")) return undefined;
  return (parsed as Record<string, unknown>[]).map((result) => block(text(result.content ?? ""), [text(result.tool), text(result.outcome ?? "")]));
}

const ACTION_PREFIX = "tool=";
const ACTION_ARGS = "\nargs=";

function action(stored: string): EvidenceInputPart[] | undefined {
  const split = stored.indexOf(ACTION_ARGS);
  if (!stored.startsWith(ACTION_PREFIX) || split < 0) return undefined;
  const storedArgs = stored.slice(split + ACTION_ARGS.length);
  const args: unknown = JSON.parse(storedArgs);
  if (!isRecord(args)) return undefined;
  const { cwd, ...rest } = args;
  const commandOnly = Object.keys(rest).length === 1 && typeof rest.command === "string";
  const head = [stored.slice(ACTION_PREFIX.length, split), commandOnly && typeof cwd === "string" ? copy.workingDirectory(cwd) : ""] as const;
  return [commandOnly ? { head, text: text(rest.command) } : block(storedArgs, head)];
}

function receipts(stored: string): EvidenceInputPart[] | undefined {
  const rows = stored.split("\n").filter((line) => line.trim()).map((line): unknown => JSON.parse(line));
  if (rows.length === 0 || !rows.every((row) => isRecord(row) && typeof row.command === "string")) return undefined;
  return (rows as Record<string, unknown>[]).map((row) => ({
    head: [text(row.state ?? ""), typeof row.status_code === "number" ? copy.exitStatus(row.status_code) : ""],
    text: text(row.command),
  }));
}

/** A status line, then the call's result as one JSON value. */
function callResult(stored: string): EvidenceInputPart[] | undefined {
  const split = stored.indexOf("\n");
  if (split <= 0 || !holdsJson(stored.slice(split + 1))) return undefined;
  return [block(stored.slice(split + 1), [stored.slice(0, split), ""])];
}

/** Diffs are stored as unified diffs; the editor colours them as it does any patch. */
function diffs(stored: string): EvidenceInputPart[] {
  return [{ text: stored, language: "diff" }];
}

const SHAPES: Readonly<Record<string, (stored: string) => EvidenceInputPart[] | undefined>> = {
  recent_results: recentResults, action, receipts, call_result: callResult, diffs,
};

/**
 * Splits an input's stored text into the blocks it is read as. Text that is
 * not in its key's stored form, such as a cut one, is one block exactly as stored.
 */
export function evidenceInputParts(item: Pick<BriefingRecordItem, "key" | "text">): EvidenceInputPart[] {
  const stored = item.text ?? "";
  if (!stored) return [];
  try {
    return SHAPES[item.key]?.(stored) ?? [block(stored)];
  } catch {
    return [{ text: stored }];
  }
}
