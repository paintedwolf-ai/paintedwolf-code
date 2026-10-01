/** Shared Files-tree path keys (`${rootId}\u0000${dir}`). */

export function filesTreeDirKey(rootId: string, dir: string): string {
  return `${rootId}\u0000${dir}`;
}

export function splitFilesTreeDirKey(
  key: string,
): { rootId: string; dir: string } | null {
  const i = key.indexOf("\u0000");
  if (i < 0) return null;
  const rootId = key.slice(0, i);
  const dir = key.slice(i + 1);
  if (!rootId || !dir) return null;
  return { rootId, dir };
}
