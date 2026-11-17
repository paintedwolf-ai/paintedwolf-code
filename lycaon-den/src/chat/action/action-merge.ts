import type { ActionDescriptor, ActionFact, ActionFactKind, ActionLink, ActionReceiptScope } from "../../api/types.ts";

/** A descriptor as a card draws it; `more` counts further distinct targets in a fold. */
export type CardAction = ActionDescriptor & { more?: number };

const FACT_ORDER: readonly ActionFactKind[] = [
  "result", "changed", "found", "took", "steps", "stopped_by", "network", "worker", "budget", "process", "captured", "containment", "loaded_by",
];

function ordered(facts: readonly ActionFact[]): ActionFact[] {
  return FACT_ORDER.flatMap((kind) => facts.find((fact) => fact.kind === kind) ?? []).slice(0, 4);
}

function receipts(actions: readonly ActionDescriptor[], pick: (action: ActionDescriptor) => readonly ActionReceiptScope[] | undefined): ActionReceiptScope[] {
  const seen = new Set<string>();
  const out: ActionReceiptScope[] = [];
  for (const scope of actions.flatMap((action) => pick(action) ?? [])) {
    const key = `${scope.program_id}\0${scope.activity_id}`;
    if (!seen.has(key)) {
      seen.add(key);
      out.push(scope);
    }
  }
  return out;
}

/**
 * One card for several host descriptors: a terminal session, or the calls a
 * command row folded. The first names what ran; the latest says how it ended.
 */
export function mergeActions(actions: readonly ActionDescriptor[]): CardAction | undefined {
  const first = actions[0];
  const last = actions.at(-1);
  if (!first || !last) return undefined;
  if (actions.length === 1) return first;
  const status = actions.some((action) => action.status === "running") ? "running"
    : actions.some((action) => action.status === "error") ? "error" : "done";
  const sequence = actions.flatMap((action) => action.facts.find((fact) => fact.kind === "steps")?.sequence ?? []);
  const steps: ActionFact = sequence.length
    ? { kind: "steps", count: actions.length, unit: "actions", sequence: sequence.slice(-8) }
    : { kind: "steps", count: actions.length, failed: actions.filter((action) => action.status === "error").length || undefined, unit: "calls" };
  const facts = ordered([steps, ...last.facts.filter((fact) => fact.kind !== "steps")]);
  const texts = new Set(actions.map((action) => action.ran.text));
  const scopes = receipts(actions, (action) => action.links.find((link) => link.kind === "receipt")?.receipts);
  const links: ActionLink[] = scopes.length ? [{ kind: "receipt", receipts: scopes }] : [];
  const output = receipts(actions, (action) => action.preview?.receipts);
  const preview = last.preview?.kind === "output" && output.length ? { ...last.preview, receipts: output } : last.preview;
  return {
    ...last,
    label: first.label,
    status,
    ran: first.ran,
    more: texts.size - 1 || undefined,
    facts,
    row: last.row.includes("steps") ? last.row : [...last.row.slice(0, 1), "steps"],
    preview,
    links: sequence.length ? last.links : links,
    opener: [...actions].reverse().find((action) => action.opener)?.opener,
  };
}
