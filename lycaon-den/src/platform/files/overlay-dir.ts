/** Shared project overlay directory for all build channels. */
export const OVERLAY_DIR = ".paintedwolf";

/** Slash-separated path under the project overlay. */
export function overlayRel(...segments: string[]): string {
  const parts = segments
    .map((seg) => seg.trim().replace(/^\/+|\/+$/g, ""))
    .filter((seg) => seg !== "");
  return [OVERLAY_DIR, ...parts].join("/");
}
