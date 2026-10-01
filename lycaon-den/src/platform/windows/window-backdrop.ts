import { invoke } from "@tauri-apps/api/core";
import type { AppearanceMode } from "../../contributions/theme-application.ts";
import { isTauriRuntime } from "../runtime.ts";

/** Pinned to `index.html` and `window_backdrop.rs`. */
export const STOCK_LIGHT_BACKDROP_HEX = "#ffffff";
export const STOCK_DARK_BACKDROP_HEX = "#191817";

export type BackdropRgb = Readonly<{ r: number; g: number; b: number }>;
export type BackdropPalette = {
  light?: string;
  dark?: string;
};

export function parseBackdropHex(value: string): BackdropRgb | null {
  const match = /^#([0-9a-f]{6})$/i.exec(value.trim());
  if (!match?.[1]) return null;
  const n = Number.parseInt(match[1], 16);
  return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
}

function stockRgb(hex: string): BackdropRgb {
  const parsed = parseBackdropHex(hex);
  if (!parsed) {
    throw new Error("invariant: stock backdrop hex");
  }
  return parsed;
}

const STOCK_LIGHT_RGB = stockRgb(STOCK_LIGHT_BACKDROP_HEX);
const STOCK_DARK_RGB = stockRgb(STOCK_DARK_BACKDROP_HEX);

export function stockBackdrop(scheme: "light" | "dark"): BackdropRgb {
  return scheme === "dark" ? STOCK_DARK_RGB : STOCK_LIGHT_RGB;
}

export function backdropForTheme(
  scheme: "light" | "dark",
  background?: string,
): BackdropRgb {
  return (background && parseBackdropHex(background)) || stockBackdrop(scheme);
}

export function syncWindowBackdrop(
  mode: AppearanceMode,
  scheme: "light" | "dark",
  palette: BackdropPalette,
): void {
  if (!isTauriRuntime()) return;
  const light = backdropForTheme("light", palette.light);
  const dark = backdropForTheme("dark", palette.dark);
  void invoke("den_set_window_backdrop", { light, dark, scheme, mode }).catch(
    (err: unknown) => {
      console.debug("[window-backdrop] set failed", err);
    },
  );
}
