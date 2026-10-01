import { paneToggleTip } from "./PaneToggle.tsx";
import { ariaKeyShortcutsForHandler } from "../../shortcuts/display-binding-for.ts";
import { Show } from "solid-js";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";

type ToggleProps = {
  onClick: () => void;
  /** Edge currently occupied by the sidebar. */
  side?: "left" | "right";
  /** Name the pane when two restore controls share an edge. */
  labelled?: boolean;
};

type IconProps = Pick<ToggleProps, "side">;

/** Mirror the chevron when the rail is on the right. */
function mirrorForSide(side: IconProps["side"]) {
  return side === "right" ? "den-theme-icon--mirror" : undefined;
}

export function SidebarCollapseIcon(props: IconProps = {}) {
  return (
    <ThemeIcon
      slot="sidebar-collapse"
      size={16}
      class={mirrorForSide(props.side)}
    />
  );
}

export function SidebarExpandIcon(props: IconProps = {}) {
  return (
    <ThemeIcon slot="sidebar-expand" size={16} class={mirrorForSide(props.side)} />
  );
}

/** Double chevrons mark automatic width-based collapse. */
export function SidebarAutomaticExpandIcon(props: IconProps = {}) {
  return (
    <span class="den-sidebar-automatic-expand-icon" aria-hidden="true">
      <SidebarExpandIcon side={props.side} />
      <SidebarExpandIcon side={props.side} />
    </span>
  );
}

/** Collapse the sidebar — lives in the aside titlebar. */
export function NavSidebarCollapseButton(props: ToggleProps) {
  return (
    <button
      type="button"
      class="den-shell-nav-sidebar-btn-collapse den-inset-icon-btn"
      data-testid="nav-collapse-btn"
      aria-label="Hide sidebar"
      data-tip={paneToggleTip("Hide sidebar", "nav.toggle")}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("nav.toggle")}
      onClick={() => props.onClick()}
    >
      <SidebarCollapseIcon side={props.side} />
    </button>
  );
}

/** Show control in the header while the rail is hidden. */
export function NavSidebarExpandButton(props: ToggleProps) {
  return (
    <button
      type="button"
      class="den-inset-icon-btn den-shell-nav-sidebar-btn-expand"
      classList={{ "den-pane-toggle--labelled": props.labelled }}
      data-testid="nav-expand-btn"
      aria-label="Show sidebar"
      data-tip={paneToggleTip("Show sidebar", "nav.toggle")}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("nav.toggle")}
      onClick={() => props.onClick()}
    >
      <SidebarExpandIcon side={props.side} />
      <Show when={props.labelled}><span>Sidebar</span></Show>
    </button>
  );
}

/** Widens the window to restore the sidebar. */
export function NavSidebarAutoRestoreButton(props: ToggleProps) {
  const label = "Widen window to restore sidebar";
  return (
    <button
      type="button"
      class="den-auto-restore-control den-inset-icon-btn den-shell-nav-sidebar-btn-expand"
      classList={{ "den-pane-toggle--labelled": props.labelled }}
      data-testid="nav-auto-restore-btn"
      aria-label={label}
      data-tip={paneToggleTip(label, "nav.toggle")}
      aria-keyshortcuts={ariaKeyShortcutsForHandler("nav.toggle")}
      onClick={() => props.onClick()}
    >
      <SidebarAutomaticExpandIcon side={props.side} />
      <Show when={props.labelled}><span>Sidebar</span></Show>
    </button>
  );
}
