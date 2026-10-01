import { splitProps } from "solid-js";
import type { JSX } from "solid-js";
import { cn } from "../../shared/cn.ts";

type DenInputKind = "default" | "rules";

type Props = {
  kind?: DenInputKind;
  class?: string;
  type?:
    | "date"
    | "datetime-local"
    | "email"
    | "month"
    | "password"
    | "search"
    | "tel"
    | "text"
    | "time"
    | "url"
    | "week";
} & Omit<JSX.InputHTMLAttributes<HTMLInputElement>, "class" | "type">;

const KIND_CLASS: Record<DenInputKind, string> = {
  default: "den-input",
  rules: "den-rules-input",
};

export function DenInput(props: Props) {
  const [local, rest] = splitProps(props, ["kind", "class", "type"]);
  const kind = () => local.kind ?? "default";
  return (
    <input
      type={local.type}
      class={cn(KIND_CLASS[kind()], local.class)}
      {...rest}
    />
  );
}
