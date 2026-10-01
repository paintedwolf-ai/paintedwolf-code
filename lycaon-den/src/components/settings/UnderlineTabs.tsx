import type { JSX } from "solid-js";
import { splitProps } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { createTablistKeyboard } from "../../platform/interaction/roving-focus.ts";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";

type Props = {
  compact?: boolean;
  class?: string;
  children: JSX.Element;
} & JSX.HTMLAttributes<HTMLDivElement>;

export function UnderlineTabs(props: Props) {
  const [local, rest] = splitProps(props, ["compact", "class", "children", "role"]);
  let tablistEl: HTMLDivElement | undefined;
  const interactive = useResidentInteractive();
  createTablistKeyboard(() => tablistEl, interactive);
  return (
    <div
      ref={tablistEl}
      {...rest}
      {...chromeProps()}
      role={local.role ?? "tablist"}
      class={
        local.compact ? "den-underline-tabs-compact" : "den-underline-tabs"
      }
      classList={{ ...(local.class ? { [local.class]: true } : {}) }}
    >
      {local.children}
    </div>
  );
}
