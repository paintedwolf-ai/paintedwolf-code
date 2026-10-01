import { isTauriRuntime, tauriPlatform, type TauriPlatform } from "../runtime.ts";

export type RevealInFileManagerRejectReason =
  | "empty_path"
  | "not_absolute"
  | "outside_jail"
  | "spawn_failed"
  | "desktop_unavailable";

export type RevealInFileManagerResult =
  | { status: "revealed" }
  | { status: "rejected"; reason: RevealInFileManagerRejectReason };

export function isAbsolutePath(
  path: string,
  platform: TauriPlatform | null = tauriPlatform(),
): boolean {
  const trimmed = path.trim();
  if (!trimmed) return false;
  if (platform === "windows") {
    return /^[a-zA-Z]:[\\/]/.test(trimmed) || trimmed.startsWith("\\\\");
  }
  return trimmed.startsWith("/");
}

/** Normalize paths for root comparison without resolving symbolic links. */
export function normalizePathForJail(
  path: string,
  platform: TauriPlatform | null = tauriPlatform(),
): string {
  const windows = platform === "windows";
  let p = path.trim();
  if (windows) p = p.replace(/\//g, "\\");
  else p = p.replace(/\\/g, "/");

  const unc = windows && p.startsWith("\\\\");
  const parts = p.split(/[/\\]+/).filter((seg) => seg.length > 0 && seg !== ".");
  const out: string[] = [];
  for (const seg of parts) {
    if (seg === "..") {
      if (out.length > 0) out.pop();
      continue;
    }
    out.push(seg);
  }

  if (windows) {
    if (unc) return `\\\\${out.join("\\")}`;
    if (out.length === 0) return "";
    // Preserve the drive root separator.
    if (out.length === 1 && /^[a-zA-Z]:$/.test(out[0]!)) {
      return `${out[0]}\\`;
    }
    return out.join("\\");
  }

  if (out.length === 0) return "/";
  return `/${out.join("/")}`;
}

/** Check whether an absolute path is within a project root. */
export function pathIsUnderProjectRoots(
  absolutePath: string,
  projectRoots: readonly string[],
  platform: TauriPlatform | null = tauriPlatform(),
): boolean {
  if (!isAbsolutePath(absolutePath, platform)) return false;
  const path = normalizePathForJail(absolutePath, platform);
  if (!path) return false;
  const windows = platform === "windows";
  const cmp = (a: string, b: string) =>
    windows ? a.toLowerCase() === b.toLowerCase() : a === b;
  const starts = (full: string, prefix: string) =>
    windows
      ? full.toLowerCase().startsWith(prefix.toLowerCase())
      : full.startsWith(prefix);

  for (const rootRaw of projectRoots) {
    if (!isAbsolutePath(rootRaw, platform)) continue;
    const root = normalizePathForJail(rootRaw, platform);
    if (!root) continue;
    if (cmp(path, root)) return true;
    const sep = windows ? "\\" : "/";
    const prefix = root.endsWith(sep) ? root : `${root}${sep}`;
    if (starts(path, prefix)) return true;
  }
  return false;
}

export type RevealInFileManagerOptions = {
  platform?: TauriPlatform | null;
  isTauri?: boolean;
  invokeReveal?: (
    absolutePath: string,
    projectRoots: readonly string[],
  ) => Promise<void>;
};

async function defaultInvokeReveal(
  absolutePath: string,
  projectRoots: readonly string[],
): Promise<void> {
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("reveal_in_file_manager", {
    absolutePath,
    projectRoots: [...projectRoots],
  });
}

export async function revealInFileManager(
  absolutePath: string,
  projectRoots: readonly string[],
  options?: RevealInFileManagerOptions,
): Promise<RevealInFileManagerResult> {
  const trimmed = absolutePath.trim();
  if (!trimmed) {
    return { status: "rejected", reason: "empty_path" };
  }

  const platform =
    options?.platform !== undefined ? options.platform : tauriPlatform();
  if (!isAbsolutePath(trimmed, platform)) {
    return { status: "rejected", reason: "not_absolute" };
  }
  if (!pathIsUnderProjectRoots(trimmed, projectRoots, platform)) {
    return { status: "rejected", reason: "outside_jail" };
  }

  const tauri =
    options?.isTauri !== undefined ? options.isTauri : isTauriRuntime();

  if (!tauri) {
    return { status: "rejected", reason: "desktop_unavailable" };
  }

  const invokeReveal = options?.invokeReveal ?? defaultInvokeReveal;
  try {
    await invokeReveal(trimmed, projectRoots);
    return { status: "revealed" };
  } catch {
    return { status: "rejected", reason: "spawn_failed" };
  }
}

/**
 * Reveals an existing absolute path the person typed or pasted into search, even outside
 * project roots. Revealing only selects the item; opening it stays jailed to project roots.
 */
export async function revealTypedPathInFileManager(
  absolutePath: string,
  options?: Pick<RevealInFileManagerOptions, "platform" | "isTauri"> & {
    invokeReveal?: (absolutePath: string) => Promise<void>;
  },
): Promise<RevealInFileManagerResult> {
  const trimmed = absolutePath.trim();
  if (!trimmed) return { status: "rejected", reason: "empty_path" };
  const platform = options?.platform !== undefined ? options.platform : tauriPlatform();
  if (!isAbsolutePath(trimmed, platform)) return { status: "rejected", reason: "not_absolute" };
  const tauri = options?.isTauri !== undefined ? options.isTauri : isTauriRuntime();
  if (!tauri) return { status: "rejected", reason: "desktop_unavailable" };
  const invokeReveal = options?.invokeReveal ?? (async (path: string) => {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("reveal_typed_path_in_file_manager", { absolutePath: path });
  });
  try {
    await invokeReveal(trimmed);
    return { status: "revealed" };
  } catch {
    return { status: "rejected", reason: "spawn_failed" };
  }
}
