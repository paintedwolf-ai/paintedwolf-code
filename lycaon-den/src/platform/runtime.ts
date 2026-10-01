export type TauriPlatform = "macos" | "windows" | "linux";

/**
 * True inside the desktop webview. The build target alone does not count: a
 * browser on the Tauri dev server shares it but has no native window or IPC.
 */
export function isTauriRuntime(): boolean {
  return (
    typeof window !== "undefined" &&
    ("__TAURI_INTERNALS__" in window || "__TAURI__" in window)
  );
}

function platformFromNavigator(): TauriPlatform | null {
  if (typeof navigator === "undefined") return null;
  const ua = navigator.userAgent.toLowerCase();
  const plat = navigator.platform.toLowerCase();
  if (plat.includes("mac") || ua.includes("mac os")) return "macos";
  if (plat.includes("linux") || ua.includes("linux")) return "linux";
  if (plat.includes("win") || ua.includes("windows")) return "windows";
  return null;
}

/** Desktop target platform, or null outside the desktop runtime. */
export function tauriPlatform(): TauriPlatform | null {
  const platform = import.meta.env.TAURI_ENV_PLATFORM;
  if (platform === "macos" || platform === "windows" || platform === "linux") {
    return platform;
  }
  if (isTauriRuntime()) return platformFromNavigator();
  return null;
}

/** Platform detected from runtime metadata or browser identity. */
export function detectedPlatform(): TauriPlatform | null {
  return tauriPlatform() ?? platformFromNavigator();
}

/** True when the target uses custom window chrome. */
export function usesCustomWindowChrome(): boolean {
  const platform = tauriPlatform();
  return platform === "macos" || platform === "linux";
}

// Deep regions include descendant hits; interactive children block dragging.
export function tauriDragRegionProps(options?: {
  deep?: boolean;
}): Record<string, string> {
  return isTauriRuntime() && usesCustomWindowChrome()
    ? { "data-tauri-drag-region": options?.deep ? "deep" : "" }
    : {};
}

const TAURI_POLL_MS = 50;

/** Wait for desktop globals during bundled startup. */
export async function waitForTauriRuntime(
  timeoutMs = 5000,
): Promise<boolean> {
  if (isTauriRuntime()) return true;
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, TAURI_POLL_MS));
    if (isTauriRuntime()) return true;
  }
  return isTauriRuntime();
}
