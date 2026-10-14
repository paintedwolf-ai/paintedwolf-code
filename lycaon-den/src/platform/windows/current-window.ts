/** This window's native handle, loaded on first use so browser runs never import it. */
export async function currentWindow() {
  const { getCurrentWindow } = await import("@tauri-apps/api/window");
  return getCurrentWindow();
}
