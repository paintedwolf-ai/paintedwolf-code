import { fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatContentPage } from "./ChatContentPage.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { assertNamedControl } from "../../test/controls.ts";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { findController, resetFindControllerForTests, setFindQuery } from "../../find/find-controller.ts";
import { FIND_BAR_ID } from "../../find/command-bar-disclosure.ts";
import { applyBackgroundProcessEvent, resetBackgroundProcessStoreForTests } from "../../chat/tool/background-process-store.ts";
import { clearChatContentCache } from "../../chat/transcript/content/chat-content-reader.ts";
import { resetPrettyDisplayStateForTests } from "../editor/pretty-display-state.ts";

afterEach(() => {
  resetFindControllerForTests();
  resetBackgroundProcessStoreForTests();
  clearChatContentCache();
  resetPrettyDisplayStateForTests();
});

describe("chat content Files pane", () => {
  it("loads complete execution records in Files without paging controls", async () => {
    const get = vi.fn().mockResolvedValueOnce({program:{id:"program"},executions:[{id:"first-record"}],executions_omitted:1,next_cursor:"next"})
      .mockResolvedValueOnce({program:{id:"program"},executions:[{id:"last-record"}],executions_omitted:0});
    const view = render(() => <ChatContentPage projectId="project" client={stubClient({getCommandProgramReceipt:get})} document={{
      kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"receipts",title:"Execution records",
      content:{kind:"command_receipts",programs:[{id:"program",activityId:"activity"}],context:'{"facts":[]}'},
    }} />);
    await waitFor(() => expect(view.container.querySelector(".cm-content")?.textContent).toContain("last-record"));
    expect(view.container.querySelector(".cm-content")?.textContent).toContain("first-record");
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("keeps an unavailable execution document out of Copy contents", async () => {
    const notices = createNoticeStore(); registerNoticePublisher(notices);
    try {
      const view = render(() => <ChatContentPage projectId="project" client={null} document={{
        kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"receipts",title:"Execution records",
        content:{kind:"command_receipts",programs:[{id:"program"}],context:"{}"},
      }} />);
      await waitFor(() => expect(view.container.querySelector(".cm-content")?.textContent).toContain("Execution records are unavailable"));
      fireEvent.click(assertNamedControl(view.getByTestId("files-readonly-copy"), "button"));
      const rows = selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "project")?.notices ?? [];
      expect(rows.some(row => row.code === "chat_content_copy_failed")).toBe(true);
    } finally { registerNoticePublisher(null); }
  });

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
    expect(assertNamedControl(view.getByTestId("files-readonly-goto"), "button")).toBeTruthy();
    expect(assertNamedControl(view.getByTestId("files-editor-wrap-toggle"), "button")).toBeTruthy();
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
    expect(assertNamedControl(view.getByTestId("files-readonly-goto"), "button")).toBeTruthy();
    expect(assertNamedControl(view.getByTestId("files-editor-wrap-toggle"), "button")).toBeTruthy();
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
      fireEvent.click(assertNamedControl(view.getByTestId("files-readonly-copy"), "button"));
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
    const find = assertNamedControl(view.getAllByTestId("files-readonly-find")[0]!, "button");
    expect(find.getAttribute("aria-expanded")).toBe("false");
    fireEvent.click(find);
    expect(find.getAttribute("aria-expanded")).toBe("true");
    expect(find.getAttribute("aria-controls")).toBe(FIND_BAR_ID);
    expect(assertNamedControl(view.getAllByTestId("files-readonly-goto")[0]!, "button").getAttribute("aria-expanded")).toBe("false");
    setFindQuery("NEEDLE");
    expect(findController.cmCount().total).toBe(0);
    setSecond({ ...second(), find: { query: "NEEDLE", caseSensitive: true, offset: 12 } });
    await waitFor(() => expect(findController.cmCount().total).toBe(2));
    expect(findController.cmCount().activeIndex).toBe(1);
    expect(findController.caseSensitive()).toBe(true);
    expect(findController.activeViewId()).toContain("second");
  });

  it("formats inline JSON/JSONL in Pretty mode while preserving read-only authority, mapped redaction marks, and original line gutter labels (FMT-AC-07, FMT-AC-13)", async () => {
    const raw = '{"emoji":"🌲","secret":"[REDACTED]"}';
    // Rune index of `[REDACTED]`: `{"emoji":"🌲","secret":"` is 23 runes (`🌲` is 1 rune, 2 UTF-16 code units).
    const view = render(() => (
      <ChatContentPage
        projectId="project"
        client={null}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "message",
          toolCallId: "call-inline",
          pane: "output",
          title: "Inline JSON",
          content: {
            kind: "inline",
            text: raw,
            redaction: {
              spans: [
                {
                  field: "tool_result.content",
                  start: 23,
                  length: 10,
                  kind: "removed_secret",
                  rule_title: "Secret Token",
                },
              ],
            },
          },
        }}
      />
    ));

    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-pretty-toggle"), "button");
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
    expect(view.container.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(view.getByTestId("files-editor-mode").getAttribute("data-editor-identity")).toBe("readonly");
    expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");

    // Toggle Pretty ON: formats JSON across multiple lines, keeps read-only authority and mapped redaction mark, and blanks continuation line numbers.
    fireEvent.click(prettyBtn);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");
    await waitFor(() =>
      expect(view.container.querySelectorAll(".cm-line").length).toBeGreaterThan(1),
    );
    expect(view.container.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(view.getByTestId("files-editor-mode").getAttribute("data-editor-identity")).toBe("readonly");
    expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");
    const gutterNums = Array.from(
      view.container.querySelectorAll(".files-line-gutter__number"),
    ).map((el) => el.textContent);
    expect(gutterNums[0]).toBe("1");
    expect(gutterNums.slice(1).every((t) => t === "")).toBe(true);
  });

  it("formats retained JSON when complete bounded pages and redaction spans match sha256, and keeps protected raw view on late/incomplete responses (FMT-AC-08)", async () => {
    const raw = '{"status":"ok","token":"[REDACTED]"}';
    const runes = Array.from(raw);
    const getChatContent = vi.fn().mockResolvedValue({
      reference: {
        field: "tool_output",
        sha256: "retained-sha",
        rows: 1,
        total_runes: runes.length,
        size_bytes: raw.length,
      },
      offset: 0,
      end_offset: runes.length,
      text: raw,
      complete: true,
      spans: [
        {
          field: "tool_result.content",
          start: raw.indexOf("[REDACTED]"),
          length: 10,
          kind: "removed_secret",
          rule_title: "Token",
        },
      ],
    });
    const view = render(() => (
      <ChatContentPage
        projectId="project"
        client={stubClient({ getChatContent })}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "message",
          toolCallId: "call-retained",
          pane: "output",
          title: "Retained JSON",
          content: {
            kind: "retained",
            reference: {
              field: "tool_output",
              sha256: "retained-sha",
              rows: 1,
              total_runes: runes.length,
              size_bytes: raw.length,
              preview_rows: [{ index: 0, offset: 0, text: raw, spans: [] }],
            },
          },
        }}
      />
    ));

    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-pretty-toggle"), "button");
    fireEvent.click(prettyBtn);
    await waitFor(() =>
      expect(view.container.querySelectorAll(".cm-line").length).toBeGreaterThan(1),
    );
    expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");
  });

  it("uses a bounded snapshot with explicit Refresh for live process output in Pretty mode and returns to live output when turned off (FMT-AC-14)", async () => {
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":1}\n',
    });

    const view = render(() => (
      <ChatContentPage
        projectId="project"
        client={null}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "message",
          toolCallId: "call-proc",
          pane: "output",
          title: "Process Output",
          content: { kind: "process", handle: "proc-1" },
        }}
      />
    ));

    expect(view.container.querySelector(".cm-content")?.textContent).toBe('{"step":1}');
    expect(view.queryByTestId("chat-process-pretty-snapshot")).toBeNull();

    // Enable Pretty: captures snapshot of `{"step":1}\n`, formats it, and shows Snapshot + Refresh.
    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-pretty-toggle"), "button");
    fireEvent.click(prettyBtn);
    await waitFor(() =>
      expect(view.getByTestId("chat-process-pretty-snapshot").textContent).toBe("Snapshot"),
    );
    const refreshBtn = assertNamedControl(
      view.getByTestId("chat-process-pretty-refresh"),
      "button",
    );
    expect(view.container.querySelector(".cm-content")?.textContent).toContain('"step": 1');

    // Incoming process chunk does NOT reformat or overwrite the snapshot while Pretty is ON.
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":2}\n',
    });
    expect(view.container.querySelector(".cm-content")?.textContent).not.toContain('"step": 2');

    // Explicit Refresh incorporates the new chunk into the formatted snapshot.
    fireEvent.click(refreshBtn);
    await waitFor(() =>
      expect(view.container.querySelector(".cm-content")?.textContent).toContain('"step": 2'),
    );

    // Turning Pretty OFF returns to live output and removes Snapshot/Refresh indicators.
    fireEvent.click(prettyBtn);
    expect(view.queryByTestId("chat-process-pretty-snapshot")).toBeNull();
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":3}\n',
    });
    await waitFor(() =>
      expect(view.container.querySelector(".cm-content")?.textContent).toContain('{"step":3}'),
    );
  });
});
