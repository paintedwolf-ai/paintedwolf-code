// @vitest-environment jsdom
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { readSourceText } from "../../test/stylesheet-source.ts";
import {
  SCREEN_READER_ATTRIBUTE,
  SCREEN_READER_CHANGED_EVENT,
  applyScreenReaderPayload,
  resetScreenReaderForTests,
  setupScreenReader,
} from "./screen-reader.ts";

const invokeMock = vi.fn();
const listenMock = vi.fn();
const isTauriRuntimeMock = vi.fn(() => false);

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invokeMock(...args),
}));

vi.mock("@tauri-apps/api/event", () => ({
  listen: (...args: unknown[]) => listenMock(...args),
}));

vi.mock("../runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntimeMock(),
}));

const stamped = () => document.documentElement.getAttribute(SCREEN_READER_ATTRIBUTE);

afterEach(() => {
  resetScreenReaderForTests();
  invokeMock.mockReset();
  listenMock.mockReset();
  isTauriRuntimeMock.mockReturnValue(false);
});

describe("screen reader fact", () => {
  it("stamps the root while a screen reader runs and clears it when it stops", () => {
    applyScreenReaderPayload({ active: true, revision: 1 });
    expect(stamped()).toBe("active");
    applyScreenReaderPayload({ active: false, revision: 2 });
    expect(stamped()).toBeNull();
  });

  it("ignores a snapshot older than the live state", () => {
    applyScreenReaderPayload({ active: true, revision: 3 });
    applyScreenReaderPayload({ active: false, revision: 2 });
    expect(stamped()).toBe("active");
  });

  it("reads nothing outside the desktop host", async () => {
    await setupScreenReader();
    expect(invokeMock).not.toHaveBeenCalled();
    expect(stamped()).toBeNull();
  });

  it("follows live changes over the boot reading", async () => {
    isTauriRuntimeMock.mockReturnValue(true);
    invokeMock.mockResolvedValueOnce({ active: false, revision: 1 });
    listenMock.mockImplementation(
      async (_event: string, handler: (e: { payload: unknown }) => void) => {
        handler({ payload: { active: true, revision: 2 } });
        return () => {};
      },
    );
    await setupScreenReader();
    expect(listenMock).toHaveBeenCalledWith(SCREEN_READER_CHANGED_EVENT, expect.any(Function));
    expect(invokeMock).toHaveBeenCalledWith("accessibility_screen_reader");
    expect(stamped()).toBe("active");
  });

  it("keeps the authored behavior when the host cannot answer", async () => {
    isTauriRuntimeMock.mockReturnValue(true);
    listenMock.mockResolvedValue(() => {});
    invokeMock.mockRejectedValueOnce(new Error("no command"));
    await setupScreenReader();
    expect(stamped()).toBeNull();
  });

  it("makes all text selectable through one rule scoped to the fact", () => {
    const chrome = readSourceText(join(import.meta.dirname, "../../styling/ui-chrome.css"));
    expect(chrome).toMatch(
      /:root\[data-den-screen-reader="active"\] \*\s*\{\s*user-select:\s*text !important;\s*\}/,
    );
    expect(chrome.match(/data-den-screen-reader/g)).toHaveLength(1);
  });
});
