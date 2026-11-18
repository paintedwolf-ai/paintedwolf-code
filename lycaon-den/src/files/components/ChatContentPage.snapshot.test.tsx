import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BackgroundProcessOutput, ChatContentPage as ContentPage, ChatContentReference } from "../../api/types.ts";
import { applyBackgroundProcessEvent, applyBackgroundProcessOutputs, getBackgroundProcessText, resetBackgroundProcessStoreForTests } from "../../chat/tool/background-process-store.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { resetEditorPrefsForTests, saveEditorLineLayout } from "../../settings/editor/editor-prefs.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { ChatContentPage } from "./ChatContentPage.tsx";

afterEach(() => { resetBackgroundProcessStoreForTests(); resetEditorPrefsForTests(); });

const processDoc = (handle: string): ChatContentDocument => ({
  kind: "tool", sessionId: "s", messageId: "m", toolCallId: handle, pane: "output", title: "Output",
  content: { kind: "process", handle },
});
const event = (process_id: string, text: string) => applyBackgroundProcessEvent({
  process_id, session_id: "s", running: true, stream: "stdout", text, end_offset: text.length,
});
const output = (process_id: string, text: string): BackgroundProcessOutput => ({
  process_id, running: true, chunks: [{ offset: 0, stream: "stdout", text }],
});

describe("process snapshot readiness", () => {
  it.each([true, false])("freezes the first prepared output with Pretty initially %s", async initial => {
    if (initial) await saveEditorLineLayout("pretty");
    let finish!: (value: BackgroundProcessOutput) => void;
    const client = stubClient({ getSessionBackgroundProcessOutput: () => new Promise<BackgroundProcessOutput>(resolve => { finish = resolve; }) });
    const view = render(() => <ChatContentPage projectId="p" document={processDoc("a")} client={client} />);
    if (!initial) fireEvent.click(view.getByTestId("files-editor-layout-pretty"));
    finish(output("a", "first"));
    const body = () => view.container.querySelector(".cm-content")?.textContent;
    await waitFor(() => expect(body()).toBe("first"));
    event("a", " second");
    expect(body()).toBe("first");
    fireEvent.click(view.getByTestId("chat-process-pretty-refresh"));
    expect(body()).toBe("first second");
  });

  it("drops overtaken preparations and never freezes the previous handle", async () => {
    await saveEditorLineLayout("pretty");
    const pending = new Map<string, (value: BackgroundProcessOutput) => void>();
    const client = stubClient({ getSessionBackgroundProcessOutput: (_session: string, handle: string) => new Promise<BackgroundProcessOutput>(resolve => pending.set(handle, resolve)) });
    const [doc, setDoc] = createSignal(processDoc("a"));
    const view = render(() => <ChatContentPage projectId="p" document={doc()} client={client} />);
    setDoc(processDoc("b"));
    event("b", "current");
    pending.get("a")!(output("a", "wrong handle"));
    pending.get("b")!(output("b", "overtaken"));
    await waitFor(() => expect(view.container.querySelector(".cm-content")?.textContent).toBe("current"));
    event("b", " later");
    expect(view.container.querySelector(".cm-content")?.textContent).toBe("current");
  });

  it("freezes prepared empty text and hydrates an evicted process", async () => {
    await saveEditorLineLayout("pretty");
    const values = Array.from({ length: 514 }, (_, i) => output(String(i), ""));
    applyBackgroundProcessOutputs("s", values);
    expect(getBackgroundProcessText("s", "0")).toBeUndefined();
    expect(getBackgroundProcessText("s", "513")).toBe("");
    const get = vi.fn(async () => output("0", "rehydrated"));
    const client = stubClient({ getSessionBackgroundProcessOutput: get });
    const [doc, setDoc] = createSignal(processDoc("513"));
    const view = render(() => <ChatContentPage projectId="p" document={doc()} client={client} />);
    event("513", "new");
    expect(view.container.querySelector(".cm-content")?.textContent).toBe("");
    expect(get).not.toHaveBeenCalled();
    setDoc(processDoc("0"));
    await waitFor(() => expect(view.container.querySelector(".cm-content")?.textContent).toBe("rehydrated"));
    expect(get).toHaveBeenCalledTimes(1);
  });
});

describe("snapshot eligibility", () => {
  const ref = (text: string): ChatContentReference => ({
    field: "content", sha256: "rev", total_runes: text.length, size_bytes: text.length, rows: 1,
    preview_rows: [{ index: 0, offset: 0, text, spans: [] }],
  });
  const retained = (reference: ChatContentReference): ChatContentDocument => ({
    ...processDoc("r"), content: { kind: "retained", reference },
  } as ChatContentDocument);
  it.each(["plain prose", '{"ok":true}'])("gates both sources with the same layout plan for %s", async text => {
    const reference = ref(text);
    const get = vi.fn(async () => ({ reference, offset: 0, end_offset: text.length, complete: true, text, spans: [] }));
    const receipt = vi.fn(async () => ({ revision: "rev", total_rows: 1, settled: true, offset: 0, end_offset: 1, complete: true, rows: [{ index: 0, text }] }));
    const client = stubClient({ getChatContent: get, getCommandReceiptDocument: receipt });
    const [doc, setDoc] = createSignal(retained(reference));
    const view = render(() => <ChatContentPage projectId="p" document={doc()} client={client} />);
    await saveEditorLineLayout("pretty");
    const check = () => expect(Boolean(view.container.querySelector(".cm-content[data-den-find-remote]"))).toBe(text === "plain prose");
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    await waitFor(check);
    setDoc({ ...processDoc("c"), content: { kind: "command_receipts", scopes: [{ programId: "prog" }] } } as ChatContentDocument);
    await waitFor(() => expect(receipt).toHaveBeenCalledTimes(2));
    await waitFor(check);
  });

  it("leaves the paged reader mounted when a canceled snapshot finishes", async () => {
    let finish!: (value: ContentPage) => void;
    const text = '{"ok":true}', reference = ref(text);
    const get = vi.fn(() => new Promise<ContentPage>(resolve => { finish = resolve; }));
    const client = stubClient({ getChatContent: get });
    const view = render(() => <ChatContentPage projectId="p" document={retained(reference)} client={client} />);
    await saveEditorLineLayout("pretty");
    await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    await saveEditorLineLayout("off");
    const paged = view.container.querySelector(".cm-den-reader");
    finish({ reference, offset: 0, end_offset: text.length, complete: true, text, spans: [] });
    await Promise.resolve();
    expect(paged).toBeTruthy();
    expect(view.container.querySelector(".cm-editor")).toBe(paged);
  });
});
