import { createRoot, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { chatContentDocumentKey, type ChatContentDocument } from "./chat-content-document.ts";
import { useChatContentAccess } from "./use-chat-content-access.ts";

const page = { revision: "a".repeat(64), total_rows: 1, settled: true, offset: 0, end_offset: 1, complete: true, rows: [{ index: 0, text: "{}" }] };
const receipt = (openSeq: number): ChatContentDocument => ({ kind: "tool", sessionId: "s", messageId: "m", toolCallId: "c", pane: "receipts", title: "Receipt",
  content: { kind: "command_receipts", scopes: [{ programId: "p" }] }, openSeq });

describe("chat content access", () => {
  it("opens a failed receipt again when its link is followed again", async () => {
    const get = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(page);
    const [document, setDocument] = createSignal(receipt(1));
    const [client] = createSignal(stubClient({ getCommandReceiptDocument: get }));
    const onError = vi.fn();
    const { access, dispose } = createRoot(dispose => ({ access: useChatContentAccess(document, client, onError), dispose }));
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    expect(access()).toBeUndefined();
    setDocument(receipt(2));
    expect(chatContentDocumentKey(receipt(2))).toBe(chatContentDocumentKey(receipt(1)));
    await vi.waitFor(() => expect(access()?.rows()).toBe(1));
    expect(get).toHaveBeenCalledTimes(2);
    setDocument(receipt(3));
    await Promise.resolve();
    expect(get).toHaveBeenCalledTimes(2);
    dispose();
  });

  it("opens a failed receipt again on a new connection", async () => {
    const get = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(page);
    const [client, setClient] = createSignal(stubClient({ getCommandReceiptDocument: get }));
    const onError = vi.fn();
    const { access, dispose } = createRoot(dispose => ({ access: useChatContentAccess(() => receipt(1), client, onError), dispose }));
    await vi.waitFor(() => expect(onError).toHaveBeenCalledTimes(1));
    setClient(stubClient({ getCommandReceiptDocument: get }));
    await vi.waitFor(() => expect(access()?.rows()).toBe(1));
    dispose();
  });
});
