import { For, Show } from "solid-js";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { drawsWindowControls } from "../../platform/windows/window-chrome-layout.ts";
import { windowFullscreen, windowMaximized } from "../../platform/windows/window-maximized.ts";

type ResizeDirection =
  | "North"
  | "South"
  | "East"
  | "West"
  | "NorthWest"
  | "NorthEast"
  | "SouthWest"
  | "SouthEast";

const DIRECTIONS: readonly ResizeDirection[] = [
  "North",
  "South",
  "East",
  "West",
  "NorthWest",
  "NorthEast",
  "SouthWest",
  "SouthEast",
];

async function startResize(direction: ResizeDirection): Promise<void> {
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    await getCurrentWindow().startResizeDragging(direction);
  } catch (err) {
    console.debug("[window-chrome] resize drag refused", err);
  }
}

/**
 * Invisible resize zones along the window's edges and corners. A window that
 * draws its own decorations has no compositor border to grab, so each zone
 * hands its press to the window system's resize gesture. Maximized and
 * fullscreen windows cannot be resized by dragging and show no zones.
 */
export function WindowResizeEdges() {
  if (!drawsWindowControls() || !isTauriRuntime()) return null;
  return (
    <Show when={!windowMaximized() && !windowFullscreen()}>
      <div class="den-window-resize-edges" aria-hidden="true" data-testid="window-resize-edges">
        <For each={DIRECTIONS}>
          {(direction) => (
            <div
              class="den-window-resize-edge"
              data-direction={direction}
              onPointerDown={(event) => {
                if (event.button !== 0) return;
                event.preventDefault();
                void startResize(direction);
              }}
            />
          )}
        </For>
      </div>
    </Show>
  );
}
