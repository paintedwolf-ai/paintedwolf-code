import { createEffect, createRoot, createSignal } from "solid-js";
import { readWindowPalette } from "../../contributions/window-color-palette.ts";
import { watchPaletteTheme } from "../../contributions/palette-cache.ts";
import { itemWindowViews } from "./item-windows.ts";
import { clientIdentity } from "../connection/client-identity.ts";
import { editorWindow } from "./editor-windows.ts";

const [browserWindowNumber, setBrowserWindowNumber] = createSignal<number | undefined>();
const WINDOW_PAINT_PROPERTIES = ["--den-current-window-caret", "--den-current-window-selection", "--den-current-window-selection-strong"] as const;

export function acceptWindowNumber(number: number | undefined): void {
  if (number !== undefined) setBrowserWindowNumber(number);
}

export function startWindowIdentityPaint(root = document.documentElement): () => void {
  return createRoot(dispose => {
    const [revision, setRevision] = createSignal(0);
    const stop = watchPaletteTheme(root, () => setRevision(value => value + 1));
    createEffect(() => {
      revision();
      const identity = editorWindow({ client_id: clientIdentity(), window_number: browserWindowNumber() }, itemWindowViews());
      const color = identity ? readWindowPalette(root)?.(identity.slot) : undefined;
      const values = [color?.caret ?? "var(--den-accent-signal)", color?.selection ?? "var(--den-selection)",
        color?.selectionStrong ?? "var(--den-selection-strong)"];
      for (const [index, property] of WINDOW_PAINT_PROPERTIES.entries()) {
        const value = values[index]!;
        if (root.style.getPropertyValue(property) !== value) root.style.setProperty(property, value);
      }
    });
    return () => { stop(); dispose(); for (const property of WINDOW_PAINT_PROPERTIES) root.style.removeProperty(property); };
  });
}
