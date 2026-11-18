import type { CommandActivity, CommandProgramReceipt } from "../../api/types.ts";
import type { ActivitySpanEntry } from "../transcript/projection/transcript-item-model.ts";

const MAX_SCOPES = 8;
const MAX_CALLS = 2048;
const MAX_BOOKKEEPING_ENTRIES = 2048;

type RowAnchor = { key: string; order: number };
type RowPlacement = { order: number; after?: RowAnchor };

export type ActivityMembership = {
  folded: boolean;
  rowKey: string;
  presentation: Pick<CommandActivity, "label" | "group" | "subject">;
};

export type ActivityCallMemory = {
  program?: CommandProgramReceipt;
  primaryActivity?: string;
  outputTarget?: string;
  activities: Map<string, ActivityMembership>;
  decisions: Set<string>;
  rows: Set<string>;
};

type DecisionPlacement = { target?: string };
type ProjectedRow = { entry: ActivitySpanEntry; key: string; sourceKey: string };

export class CommandActivityMemory {
  private readonly calls = new Map<string, ActivityCallMemory>();
  private readonly decisions = new Map<string, DecisionPlacement>();
  private readonly rows = new Map<string, RowPlacement>();
  private readonly bookkeepingRows = new Map<string, true>();
  private nextRowOrder = 0;

  call(key: string): ActivityCallMemory {
    const state: ActivityCallMemory = this.calls.get(key) ?? {
      activities: new Map(),
      decisions: new Set<string>(),
      rows: new Set<string>(),
    };
    this.calls.delete(key);
    this.calls.set(key, state);
    while (this.calls.size > MAX_CALLS) {
      const oldest = this.calls.keys().next().value!;
      const evicted = this.calls.get(oldest)!;
      for (const row of evicted.rows) this.rows.delete(row);
      for (const decision of evicted.decisions) this.decisions.delete(decision);
      this.calls.delete(oldest);
    }
    return state;
  }

  decision(key: string, followingProgramCall: string | undefined): DecisionPlacement {
    let placement = this.decisions.get(key);
    if (!placement) {
      placement = { target: followingProgramCall };
      if (followingProgramCall) this.call(followingProgramCall).decisions.add(key);
    }
    this.decisions.delete(key);
    this.decisions.set(key, placement);
    while (this.decisions.size > MAX_BOOKKEEPING_ENTRIES) {
      const oldest = this.decisions.keys().next().value!;
      const target = this.decisions.get(oldest)?.target;
      if (target) this.calls.get(target)?.decisions.delete(oldest);
      this.decisions.delete(oldest);
    }
    return placement;
  }

  showDecisionOnItsOwn(key: string, placement: DecisionPlacement): void {
    if (placement.target) this.calls.get(placement.target)?.decisions.delete(key);
    placement.target = undefined;
  }

  hasRow(key: string): boolean {
    return this.rows.has(key);
  }

  orderRows(projected: readonly ProjectedRow[]): ActivitySpanEntry[] {
    const ordered = orderPinnedRows(projected, this.rows);
    const lastSeen = ordered.findLastIndex(row => this.rows.has(row.key));
    const lastKey = ordered[lastSeen]?.key;
    let after = lastKey === undefined ? undefined : { key: lastKey, order: this.rows.get(lastKey)!.order };
    const late = lastSeen >= 0 && ordered.slice(0, lastSeen).some(row => !this.rows.has(row.key));
    const unseen = ordered.filter(row => !this.rows.has(row.key));
    const result = late ? [...ordered.filter(row => this.rows.has(row.key)), ...unseen] : ordered;

    for (const row of result) {
      if (row.entry.kind !== "tool") this.touchBookkeepingRow(row.key);
    }
    for (const row of unseen) {
      const call = row.entry.kind === "tool" ? this.calls.get(row.sourceKey) : undefined;
      if (!call && !this.bookkeepingRows.has(row.key)) continue;
      const order = this.nextRowOrder++;
      this.rows.set(row.key, { order, after: late ? after : undefined });
      call?.rows.add(row.key);
      after = { key: row.key, order };
    }
    return result.map(row => row.entry);
  }

  private touchBookkeepingRow(key: string): void {
    this.bookkeepingRows.delete(key);
    this.bookkeepingRows.set(key, true);
    while (this.bookkeepingRows.size > MAX_BOOKKEEPING_ENTRIES) {
      const oldest = this.bookkeepingRows.keys().next().value!;
      this.bookkeepingRows.delete(oldest);
      this.rows.delete(oldest);
    }
  }
}

function orderPinnedRows(projected: readonly ProjectedRow[], placements: ReadonlyMap<string, RowPlacement>): ProjectedRow[] {
  const visible = new Set(projected.map(row => row.key));
  const children = new Map<string, { row: ProjectedRow; order: number }[]>();
  const roots: ProjectedRow[] = [];

  for (const row of projected) {
    const placement = placements.get(row.key);
    const anchor = visibleAnchor(placement?.after, visible, placements);
    if (placement && anchor !== undefined) {
      let siblings = children.get(anchor);
      if (!siblings) children.set(anchor, siblings = []);
      siblings.push({ row, order: placement.order });
    } else {
      roots.push(row);
    }
  }

  const ordered: ProjectedRow[] = [];
  const pending = roots.reverse();
  while (pending.length) {
    const row = pending.pop()!;
    ordered.push(row);
    const siblings = children.get(row.key);
    if (siblings) {
      siblings.sort((a, b) => b.order - a.order);
      for (const child of siblings) pending.push(child.row);
    }
  }
  return ordered;
}

function visibleAnchor(
  pin: RowAnchor | undefined,
  visible: ReadonlySet<string>,
  placements: ReadonlyMap<string, RowPlacement>,
): string | undefined {
  // A pin survives regrouping, but expires with the anchor's remembered position.
  let anchor = pin;
  while (anchor) {
    const placement = placements.get(anchor.key);
    if (placement?.order !== anchor.order) return undefined;
    if (visible.has(anchor.key)) return anchor.key;
    anchor = placement.after;
  }
  return undefined;
}

export function createCommandActivityMemory(): CommandActivityMemory {
  return new CommandActivityMemory();
}

const scopes = new Map<string, CommandActivityMemory>();

export function commandActivityMemoryFor(scope: string): CommandActivityMemory {
  const memory = scopes.get(scope) ?? createCommandActivityMemory();
  scopes.delete(scope);
  scopes.set(scope, memory);
  while (scopes.size > MAX_SCOPES) scopes.delete(scopes.keys().next().value!);
  return memory;
}
