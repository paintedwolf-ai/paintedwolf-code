import { splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

export type DenButtonVariant =
  | "primary"
  | "secondary"
  | "ghost"
  | "danger"
  | "link"
  | "icon"
  /** Emphasized approval rejection. */
  | "no";

type Props = {
  variant: DenButtonVariant;
  compact?: boolean;
  class?: string;
} & Omit<JSX.ButtonHTMLAttributes<HTMLButtonElement>, "class">;

const VARIANT_CLASS: Record<DenButtonVariant, string> = {
  primary: "btn-primary",
  secondary: "btn-secondary",
  ghost: "btn-ghost",
  danger: "btn-danger",
  link: "btn-link",
  icon: "btn-icon",
  no: "btn-no",
};

function variantClass(variant: DenButtonVariant, compact?: boolean): string {
  return variant === "primary" && compact
    ? "btn-primary-compact"
    : cn(VARIANT_CLASS[variant], compact && "btn-compact");
}

export function DenButton(props: Props) {
  const [local, rest] = splitProps(props, ["variant", "compact", "class", "children"]);
  return (
    <button
      type="button"
      class={cn(variantClass(local.variant, local.compact), local.class)}
      {...rest}
    >
      {local.children}
    </button>
  );
}
