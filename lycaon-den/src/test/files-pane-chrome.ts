import { expect } from "vitest";
import type { FileBufferKind } from "../files/documents/project-files-buffer-kind.ts";
import { filesPaneChrome } from "../files/components/files-pane-chrome.ts";

/** Asserts a rendered pane carries exactly the chrome its buffer kind answers for. */
export function expectPaneChrome(pane: ParentNode, kind: FileBufferKind): void {
  const chrome = filesPaneChrome(kind);
  expect(pane.querySelector(".den-files-toolbar") !== null, `${kind} toolbar`).toBe(!chrome.page);
  // A breadcrumb is an address only when it names a root.
  expect(pane.querySelector(".den-files-editor__root") !== null, `${kind} breadcrumb`).toBe(chrome.address);
  expect(pane.querySelector(".den-files-editor__status") !== null, `${kind} status bar`).toBe(chrome.document);
}
