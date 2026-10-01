/** Native-drop file intake. */

import { isTauriRuntime } from "../runtime.ts";

export async function readPathBytes(
  absPath: string,
  projectRoots: readonly string[],
  maxBytes: number,
): Promise<Uint8Array> {
  if (!isTauriRuntime()) {
    throw new Error("requires the desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  const bytes = await invoke<number[]>("read_path_bytes", {
    absPath,
    projectRoots: [...projectRoots],
    maxBytes,
  });
  return Uint8Array.from(bytes);
}

/** Classify a native file for snapshot import. */
export async function importPathKind(absPath: string): Promise<"file" | "folder" | "missing"> {
  if (!isTauriRuntime()) {
    throw new Error("requires the desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<"file" | "folder" | "missing">("import_path_kind", { absPath });
}
