import type { ProjectRoot, TrustSurfaceItem } from "../../api/types.ts";

export type TrustTreeRoot = Pick<ProjectRoot, "id" | "label">;

export type TrustItemTreeNode =
  | {
      kind: "folder";
      name: string;
      path: string;
      children: TrustItemTreeNode[];
    }
  | {
      kind: "item";
      name: string;
      item: TrustSurfaceItem;
      sourceIndex: number;
    };

type MutableBranch = {
  folders: Map<string, MutableBranch>;
  items: Array<Extract<TrustItemTreeNode, { kind: "item" }>>;
};

function branch(): MutableBranch {
  return { folders: new Map(), items: [] };
}

export function trustItemTree(
  items: readonly TrustSurfaceItem[],
  roots: readonly TrustTreeRoot[],
): TrustItemTreeNode[] {
  const root = branch();
  const contributingRoots = new Set(items.map((item) => item.root_id));
  const showRootBranches = contributingRoots.size > 1;
  const rootLabels = new Map(
    roots.map((item) => [item.id, item.label?.trim() || item.id] as const),
  );

  items.forEach((item, sourceIndex) => {
    const path = item.path.trim().replaceAll("\\", "/");
    const parts = path.split("/").filter(Boolean);
    if (showRootBranches) {
      parts.unshift(`@${rootLabels.get(item.root_id) ?? item.root_id}`);
    }
    const name = parts.pop() || item.name;
    let cursor = root;
    for (const folder of parts) {
      const next = cursor.folders.get(folder) ?? branch();
      cursor.folders.set(folder, next);
      cursor = next;
    }
    cursor.items.push({ kind: "item", name, item, sourceIndex });
  });

  const finish = (current: MutableBranch, parent = ""): TrustItemTreeNode[] => {
    const direct = [...current.items].sort((a, b) => a.name.localeCompare(b.name));
    const folders = [...current.folders.entries()]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, child]) => {
        const path = parent ? `${parent}/${name}` : name;
        return {
          kind: "folder" as const,
          name,
          path,
          children: finish(child, path),
        };
      });
    return [...direct, ...folders];
  };

  return finish(root);
}
