import { Show, splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type Props = {
  for?: string;
  label?: JSX.Element | string;
  hint?: string;
  hintId?: string;
  error?: string;
  class?: string;
  /** Settings registry anchor for a field that is one setting. */
  "data-setting-id"?: string;
  children: JSX.Element;
};

export function DenField(props: Props) {
  const [local, rest] = splitProps(props, [
    "label",
    "hint",
    "hintId",
    "error",
    "class",
    "children",
  ]);
  return (
    <label class={cn("den-field", local.class)} {...rest}>
      <Show when={local.label != null}>{local.label}</Show>
      {local.children}
      <Show when={local.error}>
        <span class="den-field-error" role="alert">
          {local.error}
        </span>
      </Show>
      <Show when={!local.error && local.hint}>
        <span class="den-field-hint" id={local.hintId}>{local.hint}</span>
      </Show>
    </label>
  );
}
