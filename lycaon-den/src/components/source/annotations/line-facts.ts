/** Projects current document coordinates and host facts into navigable line rows. */
import type { Text } from "@codemirror/state";
import type { SecurityFinding } from "../../../api/types.ts";
import { relativeTimeLabel } from "../../../time/time-copy.ts";
import type { PaintedAgentMark } from "../../../files/documents/document-agent-presence.ts";
import { attributionForLine, findingLevelLabel, type AttributionMark, type FindingLineMark } from "./knowing-gutter-model.ts";

export type LineFactTarget =
  | { kind: "chat"; sessionId: string; toolCallId: string; jobId?: string; checkpointId?: string }
  | { kind: "worker"; sessionId: string; jobId: string }
  | { kind: "finding"; finding: SecurityFinding };
export type LineFactRow = {
  key: string;
  title: string;
  fact: string;
  tool?: string;
  color?: string;
  waiting?: boolean;
  target: LineFactTarget;
};
export type LineFacts = { now: LineFactRow[]; changed: LineFactRow[]; findings: LineFactRow[]; count: number };
export type LineFactsInput = {
  doc: Text;
  marks: readonly PaintedAgentMark[];
  attribution: readonly AttributionMark[];
  findings: readonly FindingLineMark[];
  sessionTitles?: Readonly<Record<string, string>>;
  activeChats?: ReadonlyMap<string, { title: string; color?: string }>;
};

export function agentMarkLines(doc: Text, painted: PaintedAgentMark): { first: number; last: number } {
  if (!painted.mark.range) return { first: 1, last: doc.lines };
  const from = Math.max(0, Math.min(painted.from, doc.length));
  const to = Math.max(from, Math.min(painted.to, doc.length));
  return { first: doc.lineAt(from).number, last: doc.lineAt(to > from ? to - 1 : from).number };
}
function span(first: number, last: number): string {
  return first === last ? `line ${first}` : `lines ${first}–${last}`;
}
const SEVERITY = ["critical", "high", "medium", "low", "info", "unknown"];
function currentFact(painted: PaintedAgentMark, doc: Text): LineFactRow {
  const { mark, color } = painted, source = mark.source;
  const { first, last } = agentMarkLines(doc, painted);
  const where = !mark.range ? "the whole file" : span(first, last);
  const base = { title: mark.chat.title, color: color?.caret };
  if (source.kind === "read") return {
    ...base, key: `${mark.chat.sessionId}:read:${source.item.id}`,
    fact: `${mark.kind === "match" ? "Search matched" : "Read"} ${where}${mark.stale ? ", changed since" : ""}`,
    tool: source.item.tool,
    target: { kind: "chat", sessionId: mark.chat.sessionId, toolCallId: source.item.tool_call_id, jobId: source.item.worker_id },
  };
  if (source.kind === "draft") {
    const item = source.item;
    const counts = item.insertions != null && item.deletions != null ? ` · +${item.insertions} −${item.deletions}` : "";
    return { ...base, key: `${mark.chat.sessionId}:draft:${item.worker_id}`,
      fact: `${item.state === "landing" ? "Landing a worker draft" : "Worker draft ready to land"}${counts}`,
      target: { kind: "worker", sessionId: mark.chat.sessionId, jobId: item.worker_id } };
  }
  const item = source.item, waiting = item.state === "awaiting_approval";
  const action = mark.kind === "insertion" ? `insert at line ${first}` : `${item.operation} ${where}`;
  return { ...base, key: `${mark.chat.sessionId}:intent:${item.id}`, waiting,
    fact: `${waiting ? "Waiting for your approval to" : "About to"} ${action}${item.to_path ? ` → ${item.to_path}` : ""}`,
    tool: item.tool, target: { kind: "chat", sessionId: mark.chat.sessionId, toolCallId: item.tool_call_id, jobId: item.worker_id,
      checkpointId: waiting ? item.checkpoint_id : undefined } };
}
function nowRank(painted: PaintedAgentMark): number {
  const source = painted.mark.source;
  return source.kind === "intent" ? (source.item.state === "awaiting_approval" ? 0 : 1) : source.kind === "draft" ? 2 : 3;
}
export function lineFacts(line: number, input: LineFactsInput): LineFacts {
  const now: LineFactRow[] = [], changed: LineFactRow[] = [], findings: LineFactRow[] = [];
  if (line < 1 || line > input.doc.lines) return { now, changed, findings, count: 0 };
  const live = input.marks.filter(mark => mark.leftAt === null);
  const seen = new Set<string>();
  for (const painted of live.filter(mark => {
    const range = agentMarkLines(input.doc, mark);
    return line >= range.first && line <= range.last;
  }).sort((a, b) => nowRank(a) - nowRank(b)
    || (a.mark.chat.sessionId === b.mark.chat.sessionId && a.mark.source.kind === "read" && b.mark.source.kind === "read" ? b.mark.source.item.sequence - a.mark.source.item.sequence : 0)
    || a.mark.chat.slot - b.mark.chat.slot || a.mark.key.localeCompare(b.mark.key))) {
    const row = currentFact(painted, input.doc);
    if (!seen.has(row.key)) { now.push(row); seen.add(row.key); }
  }
  const attr = attributionForLine(input.attribution, line);
  for (const author of [...(attr?.contributors ?? [])].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts) || b.turn - a.turn)) {
    const present = live.find(mark => mark.mark.chat.sessionId === author.sessionId);
    const chat = input.activeChats?.get(author.sessionId);
    changed.push({ key: `${author.sessionId}:${author.turn}:${author.toolCallId}`,
      title: chat?.title || present?.mark.chat.title || input.sessionTitles?.[author.sessionId]?.trim() || "Chat",
      fact: `Edited ${span(attr!.startLine, attr!.endLine)} · turn ${author.turn}${relativeTimeLabel(author.ts) ? ` · ${relativeTimeLabel(author.ts)}` : ""}`,
      color: chat?.color ?? present?.color?.caret,
      target: { kind: "chat", sessionId: author.sessionId, toolCallId: author.toolCallId },
    });
  }
  for (const finding of (input.findings.find(mark => mark.line === line)?.findings ?? []).slice()
    .sort((a, b) => SEVERITY.indexOf(a.level) - SEVERITY.indexOf(b.level))) {
    const key = finding.fingerprints.primary;
    if (findings.some(row => row.key === key)) continue;
    findings.push({ key, title: finding.message, fact: `${findingLevelLabel(finding.level)} · ${finding.tool.name}`,
      target: { kind: "finding", finding } });
  }
  return { now, changed, findings, count: now.length + changed.length + findings.length };
}
