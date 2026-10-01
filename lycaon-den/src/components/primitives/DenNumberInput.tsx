import { splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type Props = {
  class?: string;
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type" | "class">;

export function DenNumberInput(props: Props) {
  const [local, rest] = splitProps(props, ["class"]);

  return (
    <span class={cn("den-number-input", local.class)}>
      <input
        type="number"
        class="den-input den-number-input__field"
        {...rest}
      />
    </span>
  );
}
