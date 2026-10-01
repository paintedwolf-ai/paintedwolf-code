import type { NewEntryKind } from "../commands/project-files-create.ts";
import type { StickyIndex } from "./files-tree-sticky.ts";
import type { FlatTreeRow } from "./project-files-tree-flat.ts";

export type DisplayRow =
  | { kind: "entry"; key: string; entry: FlatTreeRow }
  | {
      kind: "naming";
      key: string;
      rootId: string;
      rootLabel: string;
      dir: string;
      depth: number;
      entryKind: NewEntryKind;
    }
  | { kind: "create-error"; key: string; depth: number; message: string }
  | { kind: "empty"; key: string; depth: number }
  | { kind: "error"; key: string; depth: number; message: string };

export type DisplayRowModel = StickyIndex & {
  readonly length: number;
  rowAt(index: number): DisplayRow | undefined;
  indexOfEntry(rootId: string, path: string, isDir?: boolean): number;
  indexOfKey(key: string): number;
  nextEntryIndex(from: number, delta: -1 | 1): number;
  firstEntryIndex(): number;
  lastEntryIndex(): number;
};
