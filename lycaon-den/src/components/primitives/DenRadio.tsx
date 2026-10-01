import { Show, splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type ControlProps = {
  class?: string;
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type" | "class">;

type Props = ControlProps & {
  children?: JSX.Element;
  /** Applied to the input, not the label. */
  controlClass?: string;
};

export function DenRadioControl(props: ControlProps) {
  const [local, rest] = splitProps(props, ["class"]);
  return <input type="radio" class={cn("den-radio", local.class)} {...rest} />;
}

export function DenRadio(props: Props) {
  const [local, rest] = splitProps(props, ["children", "class", "controlClass"]);
  return (
    <label class={cn("den-choice-label", local.class)}>
      <DenRadioControl class={local.controlClass} {...rest} />
      <Show when={local.children != null}>
        <span class="min-w-0">{local.children}</span>
      </Show>
    </label>
  );
}
