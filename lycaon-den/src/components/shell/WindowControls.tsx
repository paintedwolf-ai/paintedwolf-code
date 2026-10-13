import { For, Show, Switch, Match, type JSX } from "solid-js";
import {
  drawsWindowControls,
  windowChromeControls,
  type WindowChromeSide,
} from "../../platform/windows/window-chrome-layout.ts";
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

async function windowApi() {
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  return getCurrentWindow();
}

const glyph = (path: JSX.Element) => (
  <svg viewBox="0 0 10 10" width="10" height="10" aria-hidden="true" class="den-shell-window-control__glyph">
    {path}
  </svg>
);

// Each render draws its own nodes; a shared node would move between buttons.
const minimizeGlyph = () => glyph(<path d="M1 5.5h8" />);
const maximizeGlyph = () => glyph(<rect x="1.5" y="1.5" width="7" height="7" />);
const restoreGlyph = () =>
  glyph(
    <>
      <rect x="1.5" y="3.5" width="5" height="5" />
      <path d="M3.5 3.5v-2h5v5h-2" />
    </>,
  );
const closeGlyph = () => glyph(<path d="M1.5 1.5l7 7M8.5 1.5l-7 7" />);

/**
 * Drawn window controls for one window edge. Linux lays them out as the
 * desktop asks; macOS and Windows keep native buttons, so nothing renders.
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
                  aria-label="Minimize window"
                  data-testid="window-minimize"
                  onClick={() => void windowApi().then((win) => win.minimize())}
                >
                  {minimizeGlyph()}
                </button>
              </Match>
              <Match when={control === "maximize"}>
                <button
                  type="button"
                  class="den-shell-window-control"
                  aria-label={windowMaximized() ? "Restore window" : "Maximize window"}
                  data-testid="window-maximize"
                  data-maximized={windowMaximized() ? "" : undefined}
                  onClick={() => void windowApi().then((win) => win.toggleMaximize())}
                >
                  {windowMaximized() ? restoreGlyph() : maximizeGlyph()}
                </button>
              </Match>
              <Match when={control === "close"}>
                <button
                  type="button"
                  class="den-shell-window-control den-shell-window-control-close"
                  aria-label="Close window"
                  data-testid="window-close"
                  onClick={() => void windowApi().then((win) => win.close())}
                >
                  {closeGlyph()}
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
