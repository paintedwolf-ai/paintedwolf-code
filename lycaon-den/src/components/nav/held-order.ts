import { createMemo, createSignal, type Accessor } from "solid-js";

/** One row of a sectioned list; `section` groups rows, `key` identifies them. */
export type HeldEntry<T> = { key: string; section: string; item: T };

export type HeldOrder<T> = {
  /** Rows in presentation order; split by `section` to render the groups. */
  presented: Accessor<readonly HeldEntry<T>[]>;
  /** Presents the source order now, for a change the person just made here. */
  rebase: () => void;
};

/**
 * Presents `source` as it is until `holding` turns on, then keeps the order
 * the person is looking at. Held rows keep their place and section and take
 * fresh data. A row that leaves the source leaves the list: in ordinary use
 * rows only leave at the end, and anything else is a removal the person should
 * see. A new row joins the end of the list, unless `arrivesInPlace` says its
 * arrival is the person's own doing; then it takes its place in the source.
 */
export function createHeldOrder<T>(
  source: Accessor<readonly HeldEntry<T>[]>,
  holding: Accessor<boolean>,
  arrivesInPlace: (entry: HeldEntry<T>) => boolean = () => false,
): HeldOrder<T> {
  const [epoch, setEpoch] = createSignal(0);
  let held: HeldEntry<T>[] | null = null;

  const presented = createMemo((): readonly HeldEntry<T>[] => {
    epoch();
    const fresh = source();
    if (!holding()) {
      held = null;
      return fresh;
    }
    if (held === null) {
      held = [...fresh];
      return fresh;
    }
    const freshByKey = new Map(fresh.map((entry) => [entry.key, entry]));
    const next: HeldEntry<T>[] = [];
    for (const entry of held) {
      const update = freshByKey.get(entry.key);
      if (update) next.push({ ...entry, item: update.item });
    }
    const listed = new Set(next.map((entry) => entry.key));
    fresh.forEach((entry, index) => {
      if (listed.has(entry.key)) return;
      listed.add(entry.key);
      if (!arrivesInPlace(entry)) {
        next.push(entry);
        return;
      }
      // Directly after the nearest earlier source row of the same section.
      let at = next.findIndex((row) => row.section === entry.section);
      at = at < 0 ? next.length : at;
      for (let i = index - 1; i >= 0; i--) {
        const before = fresh[i]!;
        if (before.section !== entry.section) continue;
        const found = next.findIndex((row) => row.key === before.key);
        if (found >= 0) {
          at = found + 1;
          break;
        }
      }
      next.splice(at, 0, entry);
    });
    held = next;
    return next;
  });

  return {
    presented,
    rebase: () => {
      held = null;
      setEpoch((n) => n + 1);
    },
  };
}
