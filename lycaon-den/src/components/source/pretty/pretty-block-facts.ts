import { type ChangeDesc, MapMode, type Range, RangeSet, RangeValue } from "@codemirror/state";
import type { PrettyBlockFacts } from "./pretty-scan-state.ts";

export const PROTECTED_START = 1;
export const PROTECTED_END = 2;
export const EMBEDDED_END = 3;

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
  private readonly added = new Map<number, { end: number; block: boolean }>();
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

  finish(): RangeSet<BlockFact> {
    if (!this.added.size) return this.known;
    const fresh = [...this.added.entries()]
      .sort((a, b) => a[0] - b[0])
      .map(([open, fact]) => new BlockFact(fact.block).range(open, Math.max(open + 1, fact.end)));
    // An outer scan also reads nested groups whose prior facts survived.
    return this.known.update({
      add: fresh,
      filterFrom: fresh[0]!.from,
      filterTo: fresh[fresh.length - 1]!.from,
      filter: open => !this.added.has(open),
    });
  }
}

/** Locate differences among surviving events in new document coordinates. */
export function invalidateBlockFacts(
  known: RangeSet<BlockFact>,
  previous: RangeSet<FactEvent>,
  fresh: readonly Range<FactEvent>[],
  changes: ChangeDesc,
  from: number,
  end: number,
) {
  const inverse = changes.invertedDesc;
  const oldEnd = end > changes.newLength ? changes.length + 1 : inverse.mapPos(end, 1);
  const old = previous.iter(inverse.mapPos(from, -1));
  let low = Infinity, high = -1, visits = fresh.length;
  const changed = (pos: number) => {
    low = Math.min(low, pos);
    high = Math.max(high, pos);
  };
  let i = 0;
  while (old.value && old.from < oldEnd) {
    visits++;
    const before = old.value.kind === PROTECTED_END;
    const side = before ? -1 : 1;
    const mapped = changes.mapPos(old.from, side, before ? MapMode.TrackBefore : MapMode.TrackAfter);
    if (mapped === null) {
      changed(changes.mapPos(old.from, side));
    } else {
      while (i < fresh.length && fresh[i]!.from < mapped) {
        changed(fresh[i]!.from);
        i++;
      }
      const next = fresh[i];
      if (next?.from === mapped && next.value === old.value) i++;
      else changed(mapped);
    }
    old.next();
  }
  while (i < fresh.length) changed(fresh[i++]!.from);

  let opening = Infinity;
  const facts = low <= high ? known.update({
    filterFrom: low, filterTo: high,
    filter: (open, close) => {
      if (open > high || close < low) return true;
      opening = Math.min(opening, open);
      return false;
    },
  }) : known;
  return { facts, opening, through: high, visits };
}
