import type { TauriPlatform } from "../runtime.ts";
import { shortcutPlatform } from "../../shortcuts/platform.ts";

/** Where the app menu lives: the native menu bar, or a button in the window. */
export type AppMenuSurface = "native" | "in-window";

/** Linux has no global menu bar, so its menu opens from the title bar. */
export function appMenuSurface(
  platform: TauriPlatform = shortcutPlatform(),
): AppMenuSurface {
  return platform === "linux" ? "in-window" : "native";
}
