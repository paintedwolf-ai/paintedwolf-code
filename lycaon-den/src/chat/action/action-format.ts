import type { ActionDescriptor, ActionFact, ActionFactKind } from "../../api/types.ts";

/** The label each fact kind shows; the host sends only the kind. */
export const ACTION_FACT_LABELS: Readonly<Record<ActionFactKind, string>> = {
  result: "Result",
  source_observation: "File observation",
  changed: "Changed",
  found: "Found",
  took: "Took",
  steps: "Steps",
  stopped_by: "Stopped by",
  network: "Network",
  worker: "Worker",
  budget: "Budget",
  process: "Process",
  captured: "Captured",
  containment: "Containment",
  loaded_by: "Loaded by",
};

const OUTCOMES: Readonly<Record<string, string>> = {
  passed: "Passed", failed: "Failed", refused: "Refused", interrupted: "Interrupted", completed: "Completed",
  created: "Created", updated: "Updated", deleted: "Deleted", moved: "Moved", copied: "Copied",
  committed: "Committed", merged: "Merged", checked_out: "Checked out", answered: "Answered",
  approved: "Approved", declined: "Declined", pass: "Pass", fail: "Fail", captured: "Captured",
  loaded: "Loaded", canceled: "Canceled",
};

const SINGULAR: Readonly<Record<string, string>> = {
  matches: "match", files: "file", entries: "entry", lines: "line", results: "result", tools: "tool",
  findings: "finding", citations: "citation", items: "item", bytes: "byte", processes: "process",
  secrets: "secret", refs: "ref", commits: "commit", executions: "execution", calls: "call", actions: "action",
};

const LOADED_BY: Readonly<Record<string, string>> = { engine: "Local AI", name: "Exact name", none: "No match" };

function count(n: number, noun: string): string {
  return `${n.toLocaleString()} ${n === 1 ? SINGULAR[noun] ?? noun : noun}`;
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)} s`;
  const minutes = Math.floor(ms / 60_000);
  return `${minutes}m ${Math.round((ms % 60_000) / 1000)}s`;
}

function outcome(fact: ActionFact): string {
  const word = OUTCOMES[fact.outcome ?? ""] ?? "";
  if (fact.http_status !== undefined) return word ? `${word} · ${fact.http_status}` : String(fact.http_status);
  if (fact.exit_code !== undefined) return word ? `${word} · exit ${fact.exit_code}` : `Exit ${fact.exit_code}`;
  return fact.ref ? `${word} ${fact.ref}`.trim() : word;
}

function changed(fact: ActionFact): string {
  const parts: string[] = [];
  if (fact.created) parts.push(fact.created === 1 && !fact.files ? "New file" : count(fact.created, "files") + " new");
  if (fact.files) parts.push(count(fact.files, "files"));
  if (fact.added || fact.removed) parts.push(`+${(fact.added ?? 0).toLocaleString()} −${(fact.removed ?? 0).toLocaleString()}`);
  const text = parts.join(" · ");
  return fact.hunks ? `${text} in ${count(fact.hunks, "hunks")}` : text;
}

function found(fact: ActionFact): string {
  if (fact.items?.length) return fact.items.join(" · ");
  const severities = [["high", fact.high], ["medium", fact.medium], ["low", fact.low]] as const;
  if (severities.some(([, n]) => n !== undefined)) {
    return severities.filter(([, n]) => n !== undefined).map(([label, n]) => `${n!.toLocaleString()} ${label}`).join(" · ");
  }
  const base = count(fact.count ?? 0, fact.noun ?? "items");
  return fact.files ? `${base} in ${count(fact.files, "files")}` : base;
}

/** Runs of the same step collapse to "send ×2". */
function sequence(steps: readonly string[]): string {
  const runs: { step: string; n: number }[] = [];
  for (const step of steps) {
    const last = runs.at(-1);
    if (last?.step === step) last.n++;
    else runs.push({ step, n: 1 });
  }
  const text = runs.map(({ step, n }) => (n > 1 ? `${step} ×${n}` : step)).join(" → ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function steps(fact: ActionFact): string {
  if (fact.sequence?.length) return sequence(fact.sequence);
  const base = count(fact.count ?? 0, fact.unit ?? "items");
  return fact.failed ? `${base} · ${fact.failed.toLocaleString()} failed` : base;
}

function stoppedBy(fact: ActionFact): string {
  if (fact.by === "approval") {
    const who = fact.decided_by === "policy" ? "policy" : "you";
    const at = fact.decided_at ? ` · ${new Date(fact.decided_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "";
    return `Approved by ${who}${at}`;
  }
  if (fact.by === "budget") return "Budget limit";
  if (fact.by === "policy") return fact.code ? `Policy · ${fact.code}` : "Policy";
  return fact.code ? `Host · ${fact.code}` : "Host";
}

/** A fact's value as a card shows it. */
export function formatFact(fact: ActionFact): string {
  switch (fact.kind) {
    case "result": return outcome(fact);
    case "source_observation": return "File observation incomplete";
    case "changed": return changed(fact);
    case "found": return found(fact);
    case "took": return formatDuration(fact.duration_ms ?? 0);
    case "steps": return steps(fact);
    case "stopped_by": return stoppedBy(fact);
    case "network": return [(fact.hosts ?? []).join(", "), fact.egress === "proxy_only" ? "proxy only" : fact.egress === "local_only" ? "local only" : fact.egress === "direct" ? "direct" : ""].filter(Boolean).join(" · ");
    case "worker": return [fact.role, fact.state].filter(Boolean).join(" · ");
    case "budget": return fact.limit ? `${Math.round(((fact.used ?? 0) / fact.limit) * 100)}% of ${fact.limit.toLocaleString()}` : (fact.used ?? 0).toLocaleString();
    case "process": return [fact.state === "running" ? "Running" : "Finished", fact.port !== undefined ? `listening on ${fact.port}` : ""].filter(Boolean).join(" · ");
    case "captured": return fact.caption ?? "";
    case "containment": return fact.mode ?? "";
    case "loaded_by": return LOADED_BY[fact.by ?? ""] ?? "";
  }
}

/** The compact form a fact takes in the row's outcome. */
function compactFact(fact: ActionFact): string {
  switch (fact.kind) {
    case "result":
      if (fact.exit_code !== undefined && (fact.outcome === "passed" || fact.outcome === "failed")) return `Exited ${fact.exit_code}`;
      return outcome(fact);
    case "steps": return fact.sequence?.length ? count(fact.sequence.length, "actions") : count(fact.count ?? 0, "steps");
    case "changed": return fact.added || fact.removed ? `+${fact.added ?? 0} −${fact.removed ?? 0}` : count(fact.files ?? 0, "files");
    default: return formatFact(fact);
  }
}

/** The row's outcome text: the descriptor's row facts, or the status alone. */
export function formatActionRow(action: ActionDescriptor): string {
  const text = action.row
    .map((kind) => action.facts.find((fact) => fact.kind === kind))
    .filter((fact): fact is ActionFact => fact !== undefined)
    .map(compactFact)
    .filter(Boolean)
    .join(" · ");
  if (text) return text;
  return action.status === "running" ? "Running" : action.status === "error" ? "Failed" : "Finished";
}
