/** Queries for file rows in the Files tree, keyed by root-relative path. */

import { waitFor } from "@solidjs/testing-library";

function selectorFor(path: string): string {
  return `[data-testid="files-tree-file"][data-path="${path.replace(/["\\]/g, "\\$&")}"]`;
}

export function queryFileRow(path: string): HTMLElement | null {
  const found = Array.from(
    document.body.querySelectorAll<HTMLElement>(selectorFor(path)),
  );
  if (found.length > 1) {
    throw new Error(`found ${found.length} file rows for "${path}"`);
  }
  return found[0] ?? null;
}

export function getFileRow(path: string): HTMLElement {
  const found = queryFileRow(path);
  if (!found) throw new Error(`unable to find a file row for "${path}"`);
  return found;
}

export function findFileRow(
  path: string,
  options?: { timeout?: number },
): Promise<HTMLElement> {
  return waitFor(() => getFileRow(path), options);
}
