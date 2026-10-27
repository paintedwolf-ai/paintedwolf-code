// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const readClipboardImage = vi.hoisted(() => vi.fn<() => Promise<File | null>>());
vi.mock("../../utils/clipboard.ts", () => ({ readClipboardImage }));

import { attachPastedFiles } from "./composer-attachments.ts";

function paste(contents: { files?: File[]; text?: string }) {
  const items = (contents.files ?? []).map((file) => ({ kind: "file", getAsFile: () => file }));
  const event = {
    clipboardData: { items, getData: (type: string) => (type === "text/plain" ? contents.text ?? "" : "") },
    preventDefault: vi.fn(),
  };
  return event as unknown as ClipboardEvent & { preventDefault: ReturnType<typeof vi.fn> };
}

describe("pasted attachments", () => {
  beforeEach(() => readClipboardImage.mockReset());

  it("takes files the paste event carries", () => {
    const file = new File(["x"], "shot.png", { type: "image/png" });
    const attach = vi.fn();
    const event = paste({ files: [file] });
    expect(attachPastedFiles(event, attach)).toBe(true);
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(attach).toHaveBeenCalledWith([file]);
    expect(readClipboardImage).not.toHaveBeenCalled();
  });

  it("leaves text pastes to the editor", () => {
    const attach = vi.fn();
    expect(attachPastedFiles(paste({ text: "hello" }), attach)).toBe(false);
    expect(readClipboardImage).not.toHaveBeenCalled();
    expect(attach).not.toHaveBeenCalled();
  });

  it("attaches the clipboard image the shell reads when the event carries nothing", async () => {
    const image = new File(["png"], "Pasted image.png", { type: "image/png" });
    readClipboardImage.mockResolvedValue(image);
    const attach = vi.fn();
    const event = paste({});
    expect(attachPastedFiles(event, attach)).toBe(false);
    expect(event.preventDefault).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(attach).toHaveBeenCalledWith([image]));
  });

  it("attaches nothing for an empty clipboard and reports a failed read", async () => {
    readClipboardImage.mockResolvedValueOnce(null);
    const attach = vi.fn();
    attachPastedFiles(paste({}), attach);
    await vi.waitFor(() => expect(readClipboardImage).toHaveBeenCalledOnce());
    expect(attach).not.toHaveBeenCalled();
    const failure = new Error("The clipboard image is too large to paste.");
    readClipboardImage.mockRejectedValueOnce(failure);
    const failed = vi.fn();
    attachPastedFiles(paste({}), attach, failed);
    await vi.waitFor(() => expect(failed).toHaveBeenCalledWith(failure));
  });
});
