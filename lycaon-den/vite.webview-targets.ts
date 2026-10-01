import { readFileSync } from "node:fs";
import path from "node:path";

/** WebKit shipped with each macOS release; WKWebView renders with the system WebKit. */
const SAFARI_BY_MACOS_MAJOR: Readonly<Record<number, number>> = {
  13: 16,
  14: 17,
  15: 18,
  26: 26,
};

/** Lightning CSS encodes a browser version as major << 16 | minor << 8. */
export type WebviewCssTargets = { safari: number };

/** CSS targets for the oldest webview the app bundle supports, from its declared macOS minimum. */
export function webviewCssTargets(denRoot: string): WebviewCssTargets {
  const config = JSON.parse(
    readFileSync(path.join(denRoot, "src-tauri", "tauri.conf.json"), "utf8"),
  ) as { bundle?: { macOS?: { minimumSystemVersion?: string } } };
  const minimum = config.bundle?.macOS?.minimumSystemVersion;
  const major = Number.parseInt(minimum ?? "", 10);
  const safari = SAFARI_BY_MACOS_MAJOR[major];
  if (safari === undefined) {
    throw new Error(`No WebKit version is known for macOS minimum ${minimum ?? "(unset)"}.`);
  }
  return { safari: safari << 16 };
}
