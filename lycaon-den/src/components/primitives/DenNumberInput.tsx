import { splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type Props = {
  class?: string;
  /**
   * The field's number after each edit, or null once it is emptied. Text that
   * is not a number yet, such as a decimal point the locale reads differently,
   * reports nothing, so a half-typed value is never taken for zero.
   */
  onNumber?: (value: number | null) => void;
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "type" | "class">;

/**
 * A number field's value: its number, null when emptied, or undefined while the
 * text is not a number yet.
 */
export function fieldNumber(field: HTMLInputElement): number | null | undefined {
  if (field.validity.badInput) return undefined;
  if (field.value === "") return null;
  return Number.isFinite(field.valueAsNumber) ? field.valueAsNumber : undefined;
}

export function DenNumberInput(props: Props) {
  const [local, rest] = splitProps(props, ["class", "onNumber", "onInput"]);

  const onInput: JSX.InputEventHandler<HTMLInputElement, InputEvent> = (event) => {
    const handler = local.onInput;
    if (typeof handler === "function") handler(event);
    else if (handler) handler[0](handler[1], event);
    const value = fieldNumber(event.currentTarget);
    if (value !== undefined) local.onNumber?.(value);
  };

  return (
    <span class={cn("den-number-input", local.class)}>
      <input
        type="number"
        class="den-input den-number-input__field"
        {...rest}
        onInput={onInput}
      />
    </span>
  );
}
