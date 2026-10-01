import { splitProps, Show } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type Props = {
  children?: JSX.Element;
  class?: string;
  /** Applied to the input, not the label. */
  controlClass?: string;
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type" | "class">;

type ControlProps = {
  class?: string;
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type" | "class">;

export function DenCheckboxControl(props: ControlProps) {
  const [local, rest] = splitProps(props, ["class"]);
  return (
    <input
      type="checkbox"
      class={cn("den-checkbox", local.class)}
      {...rest}
    />
  );
}

export function DenCheckbox(props: Props) {
  const [local, rest] = splitProps(props, ["children", "class", "controlClass"]);
  return (
    <label class={cn("den-choice-label", local.class)}>
      <DenCheckboxControl class={local.controlClass} {...rest} />
      <Show when={local.children != null}>
        <span class="min-w-0">{local.children}</span>
      </Show>
    </label>
  );
}
