import { Show } from "solid-js";
import { ariaKeyShortcutsForHandler } from "../../shortcuts/display-binding-for.ts";
import { SidebarCollapseIcon, SidebarExpandIcon } from "./NavSidebarToggle.tsx";
import { paneToggleTip } from "./PaneToggle.tsx";

export function ContextPaneToggle(props: {
  side: "left" | "right";
  hidden?: boolean;
  labelled?: boolean;
  onClick: () => void;
}) {
  const label = () => props.hidden ? "Show context" : "Hide context";
  return (
    <button
      type="button"
      class="den-inset-icon-btn den-pane-toggle"
      classList={{ "den-pane-toggle--hide-context": !props.hidden, "den-pane-toggle--labelled": props.labelled }}
      data-testid={props.hidden ? "context-expand-btn" : "context-collapse-btn"}
      aria-label={label()}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("layout.toggleContext")}
      data-tip={paneToggleTip(label(), "layout.toggleContext")}
      onClick={() => props.onClick()}
    >
      <Show when={props.hidden} fallback={<SidebarCollapseIcon side={props.side} />}>
        <SidebarExpandIcon side={props.side} />
      </Show>
      <Show when={props.labelled}><span>Context</span></Show>
    </button>
  );
}
