import { For, createMemo, type JSX } from "solid-js";

type Entry<T> = { item: T; index: number };

/** Rows retain their identity across reorders while their keys remain present. */
export function KeyedIndex<T>(props: {
  each: readonly T[] | undefined | null;
  keyOf: (item: T) => string;
  fallback?: JSX.Element;
  children: (item: () => T, index: () => number) => JSX.Element;
}): JSX.Element {
  const entries = createMemo(() => {
    const byKey = new Map<string, Entry<T>>();
    (props.each ?? []).forEach((item, index) => {
      // Duplicate keys are distinguished by occurrence.
      const key = props.keyOf(item);
      let slot = key;
      for (let occurrence = 1; byKey.has(slot); occurrence++) slot = `${key}\u0000${occurrence}`;
      byKey.set(slot, { item, index });
    });
    return byKey;
  });
  const keys = createMemo(() => [...entries().keys()]);
  return (
    <For each={keys()} fallback={props.fallback}>
      {(key) => {
        const initial = entries().get(key);
        if (!initial) throw new Error("Keyed row is missing its initial entry");
        // A departing key reads its last entry until For disposes its row.
        const entry = createMemo<Entry<T>>((held) => entries().get(key) ?? held, initial);
        const item = createMemo(() => entry().item);
        const index = createMemo(() => entry().index);
        return props.children(item, index);
      }}
    </For>
  );
}
