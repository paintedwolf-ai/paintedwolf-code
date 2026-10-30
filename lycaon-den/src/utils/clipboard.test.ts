// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

const isTauriRuntime = vi.fn(() => false);
const tauriPlatform = vi.fn(() => "linux");
const invoke = vi.fn();

vi.mock("../platform/runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntime(),
  tauriPlatform: () => tauriPlatform(),
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...args),
}));

import {
  copyTextToClipboard,
  readClipboardImage,
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

  it("copies exposed editor credentials as the exact text the person selected", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockResolvedValue(undefined);
    const text = "DB_PASSWORD=person-chosen-value";
    await copyTextToClipboard(text);
    expect(invoke.mock.calls).toEqual([["write_clipboard_text", { text }]]);
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

  it("reads the clipboard image through the Linux shell only", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockResolvedValue(new Uint8Array([0x89, 0x50, 0x4e, 0x47]).buffer);
    const image = await readClipboardImage();
    expect(invoke).toHaveBeenCalledWith("read_clipboard_image");
    expect([image?.name, image?.type, image?.size]).toEqual(["Pasted image.png", "image/png", 4]);
    invoke.mockResolvedValue(new ArrayBuffer(0));
    expect(await readClipboardImage()).toBeNull();
    tauriPlatform.mockReturnValue("macos");
    invoke.mockClear();
    expect(await readClipboardImage()).toBeNull();
    expect(invoke).not.toHaveBeenCalled();
    tauriPlatform.mockReturnValue("linux");
  });

  it("explains why the clipboard image could not be read", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockRejectedValueOnce({ code: "image_too_large" });
    await expect(readClipboardImage()).rejects.toThrow("The clipboard image is too large to paste.");
    invoke.mockRejectedValueOnce({ code: "timeout" });
    await expect(readClipboardImage()).rejects.toThrow("The clipboard image could not be read.");
  });
});
