import { Show, createMemo } from "solid-js";
import type { Accessor, JSX } from "solid-js";

const PRESENT = Symbol("ShowLatest.present");

type Subject<T> = { key: unknown; value: T };

type Props<T> = {
  /** false, null, and undefined hide children; 0 and "" remain visible. */
  when: T | null | undefined | false;
  /** Omitting the key preserves children across all present values. */
  by?: (value: T) => unknown;
  fallback?: JSX.Element;
  children: (value: Accessor<T>) => JSX.Element;
};

/** Keeps children mounted per identity and latches their last value. */
export function ShowLatest<T>(props: Props<T>): JSX.Element {
  const value = createMemo<T | undefined>(() => {
    const when = props.when;
    return when === false || when == null ? undefined : when;
  });

  // Each mount retains its last value, including updates no child reads.
  const subject = createMemo<Subject<T> | undefined>((previous) => {
    const current = value();
    if (current === undefined) return undefined;
    const key = props.by ? props.by(current) : PRESENT;
    if (previous !== undefined && previous.key === key) {
      previous.value = current;
      return previous;
    }
    return { key, value: current };
  });

  return (
    <Show when={subject()} keyed fallback={props.fallback}>
      {(mounted) => {
        const latest: Accessor<T> = () => {
          // Live value changes update children without remounting the subject.
          const current = value();
          return subject() === mounted && current !== undefined
            ? current
            : mounted.value;
        };
        return props.children(latest);
      }}
    </Show>
  );
}
