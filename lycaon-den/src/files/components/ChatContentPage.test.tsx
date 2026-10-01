import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatContentPage } from "./ChatContentPage.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { findController, resetFindControllerForTests, setFindQuery } from "../../find/find-controller.ts";

afterEach(resetFindControllerForTests);

describe("chat content Files pane", () => {
  it("opens prepared large output in a read-only CodeMirror with the Files editor controls", async () => {
    const getRows=vi.fn();
    const view=render(()=><ChatContentPage projectId="project" client={stubClient({getChatContent:getRows})} document={{
      kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"output",title:"Read output",
      content:{kind:"retained",reference:{field:"tool_output",sha256:"content",rows:10000,total_runes:200000,size_bytes:200000,
        preview_rows:Array.from({length:64},(_,index)=>({index,offset:index*20,text:"A complete sentence.\n",spans:[]}))}},
    }} />);
    await waitFor(()=>expect(view.container.querySelector(".cm-content")?.textContent).toContain("A complete sentence."));
    expect(view.container.querySelectorAll(".cm-editor")).toHaveLength(1);
    expect(view.container.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(view.container.querySelector("header")).toBeNull();
    expect(view.container.querySelector(".den-files-toolbar")).not.toBeNull();
    expect(view.getByRole("button", {name:"Go to line"})).toBeTruthy();
    expect(view.getByRole("button", {name:"Wrap"})).toBeTruthy();
    expect(view.container.querySelectorAll(".cm-line").length).toBeLessThan(100);
    expect(getRows).not.toHaveBeenCalled();
  });

  it("uses CodeMirror for small text too", () => {
    const view=render(()=><ChatContentPage projectId="project" client={null} document={{
      kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"input",title:"Input",
      content:{kind:"inline",text:"read file.md"},
    }} />);
    expect(view.container.querySelector(".cm-content")?.textContent).toContain("read file.md");
    expect(view.container.querySelector("header")).toBeNull();
    expect(view.container.querySelector(".den-files-toolbar")).not.toBeNull();
    expect(view.getByRole("button", {name:"Go to line"})).toBeTruthy();
    expect(view.getByRole("button", {name:"Wrap"})).toBeTruthy();
  });

  it("does not copy a retained preview as if it were the complete offline document", () => {
    const view = render(() => <ChatContentPage projectId="project" client={null} document={{
      kind: "tool", sessionId: "session", messageId: "message", toolCallId: "call", pane: "output", title: "Output",
      content: { kind: "retained", reference: {
        field: "tool_output", sha256: "offline", rows: 100, total_runes: 1000, size_bytes: 1000,
        preview_rows: [{ index: 0, offset: 0, text: "Prepared preview", spans: [] }],
      } },
    }} />);
    expect(view.container.querySelector(".cm-content")?.textContent).toContain("Prepared preview");
    const notices = createNoticeStore();
    registerNoticePublisher(notices);
    try {
      fireEvent.click(view.getByRole("button", { name: "Copy contents" }));
      const rows = selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "project")?.notices ?? [];
      expect(rows).toMatchObject([{ code: "chat_content_copy_failed", message: "Reconnect to copy the complete document." }]);
      expect(view.queryByRole("alert")).toBeNull();
    } finally { registerNoticePublisher(null); }
  });


  it("keeps the mounted editor when the same retained document is opened again", () => {
    const [document, setDocument] = createSignal<ChatContentDocument>({
      kind: "tool", sessionId: "session", messageId: "message", toolCallId: "call", pane: "output", title: "Output",
      content: { kind: "retained", reference: {
        field: "tool_output", sha256: "reopened", rows: 1, total_runes: 8, size_bytes: 8,
        preview_rows: [{ index: 0, offset: 0, text: "Prepared", spans: [] }],
      } },
    });
    const getRows = vi.fn();
    const client = stubClient({ getChatContent: getRows });
    const view = render(() => <ChatContentPage projectId="project" client={client} document={document()} />);
    const editor = view.container.querySelector(".cm-content");
    setDocument(structuredClone(document()));
    expect(view.container.querySelector(".cm-content")).toBe(editor);
    expect(getRows).not.toHaveBeenCalled();
  });


  it("moves Find to the linked document while another editor has an active search", async () => {
    const first: ChatContentDocument = { kind: "approval", sessionId: "session", checkpointId: "first", pane: "details",
      title: "First", content: { kind: "inline", text: "Unrelated action" } };
    const [second, setSecond] = createSignal<ChatContentDocument>({ ...first, checkpointId: "second", title: "Second",
      content: { kind: "inline", text: "NEEDLE then NEEDLE" } });
    const view = render(() => <>
      <ChatContentPage projectId="project" client={null} document={first} />
      <ChatContentPage projectId="project" client={null} document={second()} />
    </>);
    fireEvent.click(view.getAllByRole("button", { name: "Find in file" })[0]!);
    setFindQuery("NEEDLE");
    expect(findController.cmCount().total).toBe(0);
    setSecond({ ...second(), find: { query: "NEEDLE", caseSensitive: true, offset: 12 } });
    await waitFor(() => expect(findController.cmCount().total).toBe(2));
    expect(findController.cmCount().activeIndex).toBe(1);
    expect(findController.caseSensitive()).toBe(true);
    expect(findController.activeViewId()).toContain("second");
  });

});
