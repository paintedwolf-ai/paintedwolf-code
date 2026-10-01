export const FILES_TREE_ROW_HEIGHT_PX = 26;

export type FlatTreeRow = {
  key: string;
  rootId: string;
  rootLabel: string;
  path: string;
  name: string;
  depth: number;
  isDir: boolean;
  isRoot: boolean;
  expanded?: boolean;
  /** Absent from the listing; drawn from the current comparison. */
  deleted?: boolean;
};
