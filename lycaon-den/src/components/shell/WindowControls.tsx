import { For, Show, Switch, Match, type JSX } from "solid-js";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import {
  drawsWindowControls,
  windowChromeControls,
  type WindowChromeSide,
} from "../../platform/windows/window-chrome-layout.ts";
import { currentWindow } from "../../platform/windows/current-window.ts";
import { windowMaximized } from "../../platform/windows/window-maximized.ts";
import { AppMenuButton } from "./AppMenuButton.tsx";

/** A column's claim on one window edge's drawn controls. */
export type WindowEdgeClaim = {
  side: WindowChromeSide;
  /** This column sits against the edge. */
  owner: boolean;
  /** This column keeps the edge once the narrow band drops its neighbor. */
  narrow: boolean;
};

/**
 * Drawn window controls for one window edge. Linux lays them out as the
 * desktop asks; macOS and Windows keep native buttons, so nothing renders.
 * Like native decorations they are pointer targets with accessible names,
 * never Tab stops.
 */
export function WindowControls(props: WindowEdgeClaim) {
  if (!drawsWindowControls()) return null;
  const controls = () => windowChromeControls(props.side);
  return (
    <Show when={(props.owner || props.narrow) && controls().length > 0}>
      <div
        class="den-shell-window-controls"
        data-side={props.side}
        data-narrow-only={!props.owner && props.narrow ? "" : undefined}
        data-narrow-hidden={props.owner && !props.narrow ? "" : undefined}
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
                  <ThemeIcon slot="window-minimize" size={12} />
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
                  <ThemeIcon slot={windowMaximized() ? "window-restore" : "window-maximize"} size={12} />
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
                  <ThemeIcon slot="window-close" size={12} />
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
export function soleWindowControls(): JSX.Element | undefined {
  if (!drawsWindowControls()) return undefined;
  return (
    <>
      <WindowControls side="left" owner narrow />
      <WindowControls side="right" owner narrow />
    </>
  );
}
