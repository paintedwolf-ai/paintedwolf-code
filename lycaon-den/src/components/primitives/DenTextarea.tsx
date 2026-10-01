import { splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type Props = {
  class?: string;
} & Omit<JSX.TextareaHTMLAttributes<HTMLTextAreaElement>, "class">;

/** A multi-line field on the same recipe as DenInput. */
export function DenTextarea(props: Props) {
  const [local, rest] = splitProps(props, ["class"]);
  return <textarea class={cn("den-input", local.class)} {...rest} />;
}
