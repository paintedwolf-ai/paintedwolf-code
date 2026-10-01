import { isTauriRuntime } from "../runtime.ts";

export type PathKind = "file" | "folder" | "missing";

export async function pathKind(
  absPath: string,
  projectRoots: readonly string[],
): Promise<PathKind> {
  if (!isTauriRuntime()) {
    throw new Error("requires the desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  const kind = await invoke<string>("path_kind", {
    absPath,
    projectRoots: [...projectRoots],
  });
  if (kind === "file" || kind === "folder" || kind === "missing") {
    return kind;
  }
  throw new Error(`unexpected path_kind: ${kind}`);
}
