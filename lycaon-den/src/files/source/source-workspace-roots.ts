import type {
  ProjectRoot,
  SourceWorkspaceRoot,
} from "../../api/types.ts";

/** Preserves root metadata while applying host-resolved paths. */
export function resolveSourceWorkspaceRoots(
  projectRoots: readonly ProjectRoot[],
  workspaceRoots: readonly SourceWorkspaceRoot[],
): ProjectRoot[] {
  if (workspaceRoots.length !== projectRoots.length) {
    throw new Error("Files workspace response has a different root set.");
  }

  const paths = new Map<string, string>();
  for (const root of workspaceRoots) {
    const id = root.id.trim();
    const path = root.path;
    if (!id || !path.trim() || paths.has(id)) {
      throw new Error("Files workspace response has invalid roots.");
    }
    paths.set(id, path);
  }

  return projectRoots.map((root) => {
    const path = paths.get(root.id);
    if (!path) {
      throw new Error("Files workspace response has a different root set.");
    }
    return { ...root, path };
  });
}
