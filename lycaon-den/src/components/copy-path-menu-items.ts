/** Omitted copy commands hide their menu entries. */

import type { ContextMenuItem } from "./ContextMenu.tsx";
import { contextAction, type ContextActionHandler } from "./context-actions.ts";

type CopyPathCommand = ContextActionHandler | { disabled: true; description: string };

export type CopyPathMenuOptions = {
  withLine?: boolean;
  copyAbsolute?: CopyPathCommand | null;
  copyRelative?: CopyPathCommand | null;
  copyFileName?: CopyPathCommand | null;
  copyContents?: CopyPathCommand | null;
  absoluteTestId: string;
  relativeTestId: string;
  fileNameTestId?: string;
  contentsTestId?: string;
};

export function copyPathMenuItems(
  opts: CopyPathMenuOptions,
): ContextMenuItem[] {
  const items: ContextMenuItem[] = [];
  const absolute = opts.copyAbsolute;
  if (absolute) {
    items.push(contextAction(opts.withLine ? "copyPathLine" : "copyPath", {
      testId: opts.absoluteTestId,
      ...(typeof absolute === "function" ? { onSelect: absolute } : absolute),
    }));
  }
  const relative = opts.copyRelative;
  if (relative) {
    items.push(contextAction(opts.withLine ? "copyRelativePathLine" : "copyRelativePath", {
      testId: opts.relativeTestId,
      ...(typeof relative === "function" ? { onSelect: relative } : relative),
    }));
  }
  const fileName = opts.copyFileName;
  if (fileName) {
    items.push(contextAction("copyFileName", {
      testId: opts.fileNameTestId,
      ...(typeof fileName === "function" ? { onSelect: fileName } : fileName),
    }));
  }
  const contents = opts.copyContents;
  if (contents) {
    items.push(contextAction("copyContents", {
      testId: opts.contentsTestId,
      ...(typeof contents === "function" ? { onSelect: contents } : contents),
    }));
  }
  return items;
}
