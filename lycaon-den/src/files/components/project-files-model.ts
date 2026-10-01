/** Buffer identity includes worker scope. */
export type FileBufferKey = string;

export function fileBufferKey(
  rootId: string,
  path: string,
  jobId?: string,
  fileId?: string,
): FileBufferKey {
  const overlay = jobId?.trim() ? `\u0000${jobId.trim()}` : "";
  const identity = fileId?.trim()
    ? `file:${fileId.trim()}`
    : `path:${rootId}\u0000${path}`;
  return `${identity}${overlay}`;
}

/** A walk step page is keyed by the step it presents, not by a file. */
export function walkPageBufferKey(stepKey: string): FileBufferKey {
  return `walk:${stepKey.trim()}`;
}

/** A diffs page is keyed by the comparison it presents; one page per address. */
export function diffsBufferKey(addressKey: string): FileBufferKey {
  return `diffs:${addressKey}`;
}

type ParsedFileBufferKey =
  | { kind: "file"; fileId: string; jobId?: string }
  | { kind: "path"; rootId: string; path: string; jobId?: string };

/** Decodes either stable-file or unresolved-path identity. */
export function parseFileBufferKey(key: FileBufferKey): ParsedFileBufferKey | null {
  if (key.startsWith("file:")) {
    const [fileId, jobId] = key.slice("file:".length).split("\0", 2);
    if (!fileId) return null;
    return { kind: "file", fileId, ...(jobId ? { jobId } : {}) };
  }
  if (!key.startsWith("path:")) return null;
  const [rootId, path, jobId] = key.slice("path:".length).split("\0", 3);
  if (!rootId || !path) return null;
  return { kind: "path", rootId, path, ...(jobId ? { jobId } : {}) };
}

/** Retarget one root-relative path under a file or directory move. */
export function retargetPathUnderPath(
  path: string,
  fromPath: string,
  toPath: string,
): string | null {
  if (path === fromPath) return toPath;
  if (fromPath !== "." && path.startsWith(`${fromPath}/`)) {
    return `${toPath}${path.slice(fromPath.length)}`;
  }
  return null;
}

/** Join a buffer path using the root's separator. */
export function absolutePathForBuffer(rootPath: string, path: string): string {
  const sep = rootPath.includes("\\") ? "\\" : "/";
  const base = rootPath.endsWith(sep) ? rootPath.slice(0, -1) : rootPath;
  return `${base}${sep}${sep === "\\" ? path.replaceAll("/", "\\") : path}`;
}

/** Root-relative child path for a tree node ("." parent means top level). */
export function childTreePath(dir: string, name: string): string {
  return dir === "." || dir === "" ? name : `${dir}/${name}`;
}

export function fileDisplayName(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

/** The tab that takes a closed tab's place: its right neighbour, else its left. */
export function neighborKeyAfterClose(
  orderedKeys: readonly FileBufferKey[],
  closedKey: FileBufferKey,
): FileBufferKey | null {
  const idx = orderedKeys.indexOf(closedKey);
  if (idx < 0) return null;
  const remaining = orderedKeys.filter((k) => k !== closedKey);
  return remaining[Math.min(idx, remaining.length - 1)] ?? null;
}
