// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const isTauriRuntime = vi.fn(() => false);
const invoke = vi.fn();

vi.mock("../platform/runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntime(),
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...args),
}));

import {
  copyTextToClipboard,
  readClipboardText,
  writeClipboardText,
} from "./clipboard.ts";

describe("clipboard", () => {
  afterEach(() => {
    isTauriRuntime.mockReturnValue(false);
    invoke.mockReset();
    vi.unstubAllGlobals();
  });

  it("reads through the host command in the desktop shell", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockResolvedValue("from-os");
    await expect(readClipboardText()).resolves.toEqual({
      readable: true,
      text: "from-os",
    });
    expect(invoke).toHaveBeenCalledWith("read_clipboard_text");
  });

  it("writes through the host command in the desktop shell", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockResolvedValue(undefined);
    await expect(writeClipboardText("/tmp/draft")).resolves.toBeUndefined();
    expect(invoke).toHaveBeenCalledWith("write_clipboard_text", {
      text: "/tmp/draft",
    });
  });

  it("reads navigator.clipboard outside the desktop shell", async () => {
    const readText = vi.fn(async () => "from-web");
    vi.stubGlobal("navigator", { clipboard: { readText } });
    await expect(readClipboardText()).resolves.toEqual({
      readable: true,
      text: "from-web",
    });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("writes navigator.clipboard outside the desktop shell", async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal("navigator", { clipboard: { writeText } });
    await expect(writeClipboardText("src/main.ts")).resolves.toBeUndefined();
    expect(writeText).toHaveBeenCalledWith("src/main.ts");
    expect(invoke).not.toHaveBeenCalled();
  });

  it("reports an empty clipboard as readable and empty", async () => {
    const readText = vi.fn(async () => "");
    vi.stubGlobal("navigator", { clipboard: { readText } });
    await expect(readClipboardText()).resolves.toEqual({ readable: true, text: "" });
  });

  it("says it could not read rather than reporting empty", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockRejectedValue(new Error("denied"));
    await expect(readClipboardText()).resolves.toEqual({ readable: false });
  });

  it("keeps best-effort copies non-throwing when the host refuses the write", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockRejectedValue(new Error("denied"));
    await expect(copyTextToClipboard("src/main.ts")).resolves.toBeUndefined();
  });
});
