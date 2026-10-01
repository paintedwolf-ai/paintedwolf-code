/** Native open/save panels through the `pick_path` bridge. */

export type NativePathTarget =
  | { kind: "folder" }
  | { kind: "file"; filter?: { name: string; extensions: string[] } }
  | { kind: "save"; fileName: string };

/**
 * `grant` is the single-use handle carrying byte access; `path` is for display
 * and for locations handed to the sidecar. Folder picks carry no grant.
 */
export type NativePick = {
  path: string;
  grant: string | null;
};

let pickInFlight = false;

export async function nativePathDialog(
  title: string,
  target: NativePathTarget,
): Promise<NativePick | null> {
  // Concurrent panels cancel each other; one open at a time.
  if (pickInFlight) return null;
  pickInFlight = true;
  try {
    const { invoke } = await import("@tauri-apps/api/core");
    const picked = await invoke<NativePick | null>("pick_path", {
      options: { title, target },
    });
    const path = typeof picked?.path === "string" ? picked.path.trim() : "";
    if (!path) return null;
    return { path, grant: picked?.grant ?? null };
  } finally {
    pickInFlight = false;
  }
}

/** Test-only: set the in-flight latch. */
export function setNativePathDialogBusyForTests(busy: boolean): void {
  pickInFlight = busy;
}
