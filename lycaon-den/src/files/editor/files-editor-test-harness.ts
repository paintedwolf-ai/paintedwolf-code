import "../../test/document-outbox-fixture.ts";

import {
  filesViewTestFakes,
  resetProjectFilesViewTest,
} from "../components/project-files-view-test-harness.ts";
import {
  beforeEach,
  vi,
} from "vitest";

export const { openInExternalEditor, revealInFileManager } = filesViewTestFakes();

const hoisted = vi.hoisted(() => ({
  copyTextToClipboard: vi.fn(),
  desktopRuntime: vi.fn(() => false),
}));
export const { copyTextToClipboard, desktopRuntime } = hoisted;

vi.mock("../../platform/runtime.ts", async (original) => ({
  ...await original<typeof import("../../platform/runtime.ts")>(),
  isTauriRuntime: hoisted.desktopRuntime,
}));

vi.mock("../../utils/clipboard.ts", () => ({
  copyTextToClipboard: (...args: unknown[]) => hoisted.copyTextToClipboard(...args),
}));

export function setupFilesEditorTests() {
  beforeEach(() => {
    resetProjectFilesViewTest();
    copyTextToClipboard.mockReset();
    desktopRuntime.mockReturnValue(false);
  });
}
