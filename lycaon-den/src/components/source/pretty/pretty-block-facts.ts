import { type ChangeDesc, MapMode, type Range, RangeSet, RangeValue } from "@codemirror/state";
import type { PrettyBlockFacts } from "./pretty-scan-state.ts";

export const PROTECTED_START = 1;
export const PROTECTED_END = 2;

export class FactEvent extends RangeValue {
  private static readonly kinds = new Map<number, FactEvent>();

  static of(kind: number): FactEvent {
    let value = this.kinds.get(kind);
    if (!value) {
      value = new FactEvent(kind);
      this.kinds.set(kind, value);
    }
    return value;
  }

  private constructor(readonly kind: number) { super(); }
}

export const NO_EVENTS = RangeSet.empty as RangeSet<FactEvent>;

export class BlockFact extends RangeValue {
  constructor(readonly block: boolean) {
    super();
  }
  override eq(other: RangeValue): boolean {
    return other instanceof BlockFact && other.block === this.block;
  }
}

/** Facts read from a set and new ones collected during one scan. */
export class FactStore implements PrettyBlockFacts {
  readonly added = new Map<number, { end: number; block: boolean }>();
  constructor(private readonly known: RangeSet<BlockFact>) {}

  get(open: number): boolean | undefined {
    const fresh = this.added.get(open);
    if (fresh) return fresh.block;
    let found: boolean | undefined;
    this.known.between(open, open, (from, _to, value) => {
      if (from === open) {
        found = value.block;
        return false;
      }
      return;
    });
    return found;
  }

  set(open: number, end: number, block: boolean): void {
    if (!this.added.has(open)) this.added.set(open, { end, block });
  }

  ranges(): Range<BlockFact>[] {
    return [...this.added.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([open, fact]) => new BlockFact(fact.block).range(open, Math.max(open + 1, fact.end)));
  }
}


/** Compare event positions in the edited document. A deleted boundary never matches a survivor. */
export function changedFactEvents(
  previous: RangeSet<FactEvent>,
  fresh: readonly Range<FactEvent>[],
  changes: ChangeDesc,
  from: number,
  end: number,
): { from: number; to: number; visits: number } {
  const inverse = changes.invertedDesc;
  const oldFrom = inverse.mapPos(from, -1);
  const oldEnd = end > changes.newLength ? changes.length + 1 : inverse.mapPos(end, 1);
  const old: number[] = [];
  for (const cursor = previous.iter(oldFrom); cursor.value && cursor.from < oldEnd; cursor.next()) {
    const kind = cursor.value.kind;
    const before = kind === PROTECTED_END;
    const side = before ? -1 : 1;
    const pos = changes.mapPos(cursor.from, side, before ? MapMode.TrackBefore : MapMode.TrackAfter);
    old.push(pos === null ? changes.mapPos(cursor.from, side) * 256 + kind + 128 : pos * 256 + kind);
  }
  const events = fresh.map(range => range.from * 256 + range.value.kind);
  let first = 0, a = old.length, b = events.length;
  while (first < a && first < b && old[first] === events[first]) first++;
  while (a > first && b > first && old[a - 1] === events[b - 1]) { a--; b--; }
  let low = Infinity, high = -1;
  const include = (value: number) => {
    const pos = Math.floor(value / 256);
    low = Math.min(low, pos);
    high = Math.max(high, pos);
  };
  for (let i = first; i < a; i++) include(old[i]!);
  for (let i = first; i < b; i++) include(events[i]!);
  return { from: low, to: high, visits: old.length + events.length };
}
