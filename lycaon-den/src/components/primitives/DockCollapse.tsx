import { Show, createSignal, type JSX } from "solid-js";
import { Dynamic } from "solid-js/web";
import { ThemeIcon } from "./ThemeIcon.tsx";

export type DockCollapse = {
  minimized: () => boolean;
  setMinimized: (next: boolean) => void;
};

export function createDockCollapse(p: {
  /** Omit to keep the state local to the surface. */
  minimized?: () => boolean | undefined;
  onMinimizedChange?: (next: boolean) => void;
}): DockCollapse {
  const [local, setLocal] = createSignal(false);
  return {
    minimized: () => p.minimized?.() ?? local(),
    setMinimized: (next) => {
      setLocal(next);
      p.onMinimizedChange?.(next);
    },
  };
}

export function DockCollapseHead(p: {
  collapse: DockCollapse;
  /** Element for the expanded strip; minimized is always a button. */
  tag?: "div" | "header";
  /** Carried by the strip in both states. */
  class?: string;
  /** Held on the first line in both states. */
  lead?: () => JSX.Element;
  collapsedLead?: () => JSX.Element;
  /** Wraps between lead and minimize; items center their first line in `--den-dock-collapse-row`. */
  expanded?: () => JSX.Element;
  /** One line naming what the minimized strip hides. */
  peek: string;
  expandLabel: string;
  minimizeLabel: string;
  expandTestid: string;
  minimizeTestid: string;
}) {
  return (
    <Show
      when={!p.collapse.minimized()}
      fallback={
        <button
          type="button"
          class={`den-dock-collapse-head den-dock-collapse-restore ${p.class ?? ""}`}
          data-testid={p.expandTestid}
          aria-label={p.expandLabel}
          aria-expanded="false"
          onClick={() => p.collapse.setMinimized(false)}
        >
          {p.lead?.()}
          {p.collapsedLead?.()}
          <span class="den-dock-collapse-peek">{p.peek}</span>
          <ThemeIcon slot="chevron-up" size={14} />
        </button>
      }
    >
      <Dynamic
        component={p.tag ?? "div"}
        class={`den-dock-collapse-head ${p.class ?? ""}`}
      >
        <Show when={p.lead}>
          {(lead) => <span class="den-dock-collapse-anchor">{lead()()}</span>}
        </Show>
        <Show when={p.expanded}>
          {(expanded) => <div class="den-dock-collapse-head-body">{expanded()()}</div>}
        </Show>
        <span class="den-dock-collapse-anchor den-dock-collapse-anchor--end">
          <button
            type="button"
            class="den-dock-collapse-min den-icon-target"
            data-testid={p.minimizeTestid}
            aria-label={p.minimizeLabel}
            aria-expanded="true"
            onClick={() => p.collapse.setMinimized(true)}
          >
            <ThemeIcon slot="chevron-down" size={14} />
          </button>
        </span>
      </Dynamic>
    </Show>
  );
}

/** The retracting region; its children stay mounted while it is closed. */
export function DockCollapseBody(p: { children: JSX.Element }) {
  return (
    <div class="den-dock-collapse-body">
      <div class="den-dock-collapse-body-inner">{p.children}</div>
    </div>
  );
}
