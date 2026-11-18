import { For, Show, Switch, Match, type JSX } from "solid-js";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  windowChromeControls,
  windowEdgeChrome,
} from "../../platform/windows/window-chrome-layout.ts";
import { currentWindow } from "../../platform/windows/current-window.ts";
import { windowMaximized } from "../../platform/windows/window-maximized.ts";
import { AppMenuButton } from "./AppMenuButton.tsx";
import { windowEdgeBandAttributes, type WindowEdgeClaim } from "./window-edges.ts";

/**
 * The controls the shell draws for one window edge, laid out as the desktop
 * asks. An edge whose buttons the OS overlays renders nothing. Like native
 * decorations they are pointer targets with accessible names, never Tab stops.
 */
export function WindowControls(props: WindowEdgeClaim) {
  const controls = () => windowChromeControls(props.side);
  return (
    <Show when={props.chrome === "drawn" && (props.owner || props.narrow)}>
      <div
        class="den-shell-window-controls"
        data-side={props.side}
        {...windowEdgeBandAttributes(props)}
        data-testid={`window-controls-${props.side}`}
      >
        <For each={controls()}>
          {(control) => (
            <Switch>
              <Match when={control === "appmenu"}>
                <AppMenuButton side={props.side} />
              </Match>
              <Match when={control === "minimize"}>
                <button
                  type="button"
                  class="den-shell-window-control"
                  tabindex="-1"
                  aria-label="Minimize window"
                  data-testid="window-minimize"
                  onClick={() => void currentWindow().then((win) => win.minimize())}
                >
                  <ThemeIcon slot="window-minimize" size={14} />
                </button>
              </Match>
              <Match when={control === "maximize"}>
                <button
                  type="button"
                  class="den-shell-window-control"
                  tabindex="-1"
                  aria-label={windowMaximized() ? "Restore window" : "Maximize window"}
                  data-testid="window-maximize"
                  data-maximized={windowMaximized() ? "" : undefined}
                  onClick={() => void currentWindow().then((win) => win.toggleMaximize())}
                >
                  <ThemeIcon slot={windowMaximized() ? "window-restore" : "window-maximize"} size={14} />
                </button>
              </Match>
              <Match when={control === "close"}>
                <button
                  type="button"
                  class="den-shell-window-control den-shell-window-control-close"
                  tabindex="-1"
                  aria-label="Close window"
                  data-testid="window-close"
                  onClick={() => void currentWindow().then((win) => win.close())}
                >
                  <ThemeIcon slot="dismiss" size={14} />
                </button>
              </Match>
            </Switch>
          )}
        </For>
      </div>
    </Show>
  );
}

/** Both edges for a surface that fills the window on its own. */
export function soleWindowControls(): JSX.Element {
  return (
    <For each={["left", "right"] as const}>
      {(side) => (
        <ShowLatest when={windowEdgeChrome(side)}>
          {(chrome) => <WindowControls side={side} chrome={chrome()} owner narrow />}
        </ShowLatest>
      )}
    </For>
  );
}
