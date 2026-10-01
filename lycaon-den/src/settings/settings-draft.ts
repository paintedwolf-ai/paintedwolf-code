import { batch, createSignal, untrack } from "solid-js";

/** A JSON settings draft advances its baseline without replacing unacknowledged edits. */
export function createSettingsDraft<T>(initial: T) {
  const [value, set] = createSignal(initial);
  const [baseline, setBaseline] = createSignal(JSON.stringify(initial));
  const dirty = () => JSON.stringify(value()) !== baseline();
  return {
    value,
    set,
    dirty,
    receive(next: T, synchronize: (apply: () => void) => void) {
      const encoded = JSON.stringify(next);
      const apply = () => batch(() => {
        setBaseline(encoded);
        set(() => JSON.parse(encoded) as T);
      });
      if (untrack(dirty)) setBaseline(encoded);
      else synchronize(apply);
    },
  };
}
