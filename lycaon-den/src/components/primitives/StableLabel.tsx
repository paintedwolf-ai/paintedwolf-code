import { For } from "solid-js";

type Props = {
  label: string;
  /** Every text this label can show, or its widest form; the label keeps the widest one's width. */
  reserve: readonly string[];
  align?: "start" | "center";
  class?: string;
  testId?: string;
};

/** Tabular digits share one advance, so a run of zeros as long as `value` reserves any number up to it. */
export function widestDigits(value: number, minDigits = 1): string {
  return "0".repeat(Math.max(minDigits, String(Math.max(0, Math.trunc(value))).length));
}

/**
 * Text that changes without moving what sits beside it. The reserved texts
 * share the label's grid cell, hidden, so the cell is as wide as the widest
 * of them whatever the label currently says.
 */
export function StableLabel(props: Props) {
  return (
    <span class={`den-stable-label ${props.class ?? ""}`} data-align={props.align ?? "start"}>
      <For each={props.reserve}>
        {(text) => (
          <span class="den-stable-label-sizer" aria-hidden="true">
            {text}
          </span>
        )}
      </For>
      <span data-testid={props.testId}>{props.label}</span>
    </span>
  );
}
