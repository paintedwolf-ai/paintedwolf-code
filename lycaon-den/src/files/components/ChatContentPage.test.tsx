import { expectPaneChrome } from "./files-pane-chrome-test.ts";
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
import { resetEditorPrefsForTests, saveEditorLineLayout } from "../../settings/editor/editor-prefs.ts";
import { languageLabelForDetectedLanguage, PLAIN_TEXT_LABEL } from "../../components/source/editor/codemirror-lang.ts";

afterEach(() => {
  resetFindControllerForTests();
  resetBackgroundProcessStoreForTests();
  clearChatContentCache();
  resetEditorPrefsForTests();
});

describe("chat content Files pane", () => {
  const receiptDocument = (scopes = [{ programId: "program", activityId: "activity" }]): ChatContentDocument => ({
    kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"receipts",title:"Receipt",language:"jsonl",
    content:{kind:"command_receipts",scopes},
  });
  const receiptPage = (total: number, settled = true) => vi.fn(async (_session: string, _scopes: readonly string[], window: { offset: number; limit: number }) => ({
    revision:"a".repeat(64),total_rows:total,settled,offset:window.offset,end_offset:Math.min(total,window.offset+window.limit),complete:window.offset+window.limit>=total,
    rows:Array.from({length:Math.max(0,Math.min(total,window.offset+window.limit)-window.offset)},(_,index)=>({index:window.offset+index,text:`{"type":"execution","row":${window.offset+index}}`})),
  }));

  it("opens a large receipt with one request and pages the rest on demand", async () => {
    const get = receiptPage(65537);
    const view = render(() => <ChatContentPage projectId="project" client={stubClient({getCommandReceiptDocument:get})} document={receiptDocument()} />);
    await waitFor(() => expect(view.container.querySelector(".cm-content")?.textContent).toContain('"row":0'));
    expect(get).toHaveBeenCalledTimes(1);
    expect(get.mock.calls[0]?.slice(1, 3)).toEqual([["program:activity"], { offset: 0, limit: 64 }]);
    expectPaneChrome(view.container, "chat");
    expect(view.container.querySelector(".den-files-editor__status-language")?.textContent).toBe(languageLabelForDetectedLanguage("jsonl", ""));
    expect(view.queryByTestId("chat-process-pretty-snapshot")).toBeNull();
  });

  it("marks a receipt of a running program as a snapshot that refreshes", async () => {
    const get = receiptPage(3, false);
    const view = render(() => <ChatContentPage projectId="project" client={stubClient({getCommandReceiptDocument:get})} document={receiptDocument()} />);
    await waitFor(() => expect(view.getByTestId("chat-process-pretty-snapshot")).toBeTruthy());
    fireEvent.click(assertNamedControl(view.getByTestId("chat-process-pretty-refresh"), "button"));
    await waitFor(() => expect(get).toHaveBeenCalledTimes(2));
    expect(get.mock.calls[1]?.[2]).toEqual({ offset: 0, limit: 64 });
  });

  it("reports Copy of a receipt that is not open", async () => {
    const notices = createNoticeStore(); registerNoticePublisher(notices);
    try {
      const view = render(() => <ChatContentPage projectId="project" client={null} document={receiptDocument()} />);
      fireEvent.click(assertNamedControl(view.getByTestId("files-readonly-copy"), "button"));
      const rows = selectProjectNoticeGroups(notices.index()).find(group => group.projectId === "project")?.notices ?? [];
      expect(rows.some(row => row.code === "chat_content_copy_failed")).toBe(true);
    } finally { registerNoticePublisher(null); }
  });

  it("shows Plain text for content without a language or path", () => {
    const view = render(() => <ChatContentPage projectId="project" client={null} document={{kind:"tool",sessionId:"session",messageId:"message",toolCallId:"call",pane:"output",title:"Output",content:{kind:"inline",text:"plain"}}} />);
    expect(view.container.querySelector(".den-files-editor__status-language")?.textContent).toBe(PLAIN_TEXT_LABEL);
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
    expect(assertNamedControl(view.getByTestId("files-editor-layout-wrap"), "button")).toBeTruthy();
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
    expect(assertNamedControl(view.getByTestId("files-editor-layout-wrap"), "button")).toBeTruthy();
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
                  kind: "secret",
                  rule_title: "Secret Token",
                },
              ],
            },
          },
        }}
      />
    ));

    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-layout-pretty"), "button");
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
    expect(view.container.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(view.getByTestId("files-editor-mode").getAttribute("data-editor-identity")).toBe("view-only");
    expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");

    // Toggle Pretty ON: applies native Break/Indent decorations on the raw document,
    // keeps read-only authority and raw-offset redaction mark,
    // and keeps original source line gutter label ("1").
    fireEvent.click(prettyBtn);
    await waitFor(() => expect(prettyBtn.getAttribute("aria-pressed")).toBe("true"));
    await waitFor(() =>
      expect(view.container.querySelectorAll(".pw-pretty-break").length).toBe(3),
    );
    expect(view.container.querySelectorAll(".pw-pretty-line").length).toBe(2);
    expect(view.container.querySelector(".cm-content")?.getAttribute("contenteditable")).toBe("false");
    expect(view.getByTestId("files-editor-mode").getAttribute("data-editor-identity")).toBe("view-only");
    expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");
    const gutterNums = Array.from(
      view.container.querySelectorAll(".files-line-gutter__number[data-line]"),
    ).map((el) => el.textContent);
    expect(gutterNums[0]).toBe("1");
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
          kind: "secret",
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

    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-layout-pretty"), "button");
    fireEvent.click(prettyBtn);
    await waitFor(() =>
      expect(view.container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]"),
    );
    expect(view.container.querySelectorAll(".pw-pretty-break").length).toBe(3);
    expect(view.container.querySelector(".cm-content")?.textContent).toContain('{"status":"ok","token":"[REDACTED]"}');
  });

  it("uses a bounded snapshot with explicit Refresh for live process output in Pretty mode and returns to live output when turned off (FMT-AC-14)", async () => {
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":1}\n',
      end_offset: 11,
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

    // Enable Pretty: captures snapshot of `{"step":1}\n`, renders native breaks, and shows Snapshot + Refresh.
    const prettyBtn = assertNamedControl(view.getByTestId("files-editor-layout-pretty"), "button");
    fireEvent.click(prettyBtn);
    await waitFor(() =>
      expect(view.getByTestId("chat-process-pretty-snapshot").textContent).toBe("Snapshot"),
    );
    const refreshBtn = assertNamedControl(
      view.getByTestId("chat-process-pretty-refresh"),
      "button",
    );
    expect(view.container.querySelectorAll(".pw-pretty-break").length).toBe(2);
    expect(view.container.querySelector(".cm-content")?.textContent).toContain('{"step":1}');

    // Incoming process chunk does NOT reformat or overwrite the snapshot while Pretty is ON.
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":2}\n',
      end_offset: 22,
    });
    expect(view.container.querySelector(".cm-content")?.textContent).not.toContain('{"step":2}');

    // Explicit Refresh incorporates the new chunk into the formatted snapshot.
    fireEvent.click(refreshBtn);
    await waitFor(() =>
      expect(view.container.querySelector(".cm-content")?.textContent).toContain('{"step":2}'),
    );
    expect(view.container.querySelectorAll(".pw-pretty-break").length).toBe(4);

    // Leaving Pretty returns to live output and removes Snapshot/Refresh indicators.
    fireEvent.click(view.getByTestId("files-editor-layout-off"));
    expect(view.queryByTestId("chat-process-pretty-snapshot")).toBeNull();
    expect(view.container.querySelectorAll(".pw-pretty-break").length).toBe(0);
    applyBackgroundProcessEvent({
      session_id: "session",
      process_id: "proc-1",
      running: true,
      stream: "stdout",
      text: '{"step":3}\n',
      end_offset: 33,
    });
    await waitFor(() =>
      expect(view.container.querySelector(".cm-content")?.textContent).toContain('{"step":3}'),
    );
  });

  it("reveals an inline offset, copies inline content, and opens Go to Line on paged receipts", async () => {
    const inline = render(() => (
      <ChatContentPage
        projectId="project"
        client={null}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "message",
          toolCallId: "call-reveal",
          pane: "output",
          title: "Reveal",
          revealOffset: 6,
          content: { kind: "inline", text: "first\nsecond\n" },
        }}
      />
    ));
    expect(inline.container.querySelector(".cm-content")?.textContent).toContain("second");
    fireEvent.click(assertNamedControl(inline.getByTestId("files-readonly-copy"), "button"));
    inline.unmount();

    const get = receiptPage(65537);
    const paged = render(() => (
      <ChatContentPage projectId="project" client={stubClient({ getCommandReceiptDocument: get })} document={receiptDocument()} />
    ));
    await waitFor(() => expect(paged.container.querySelector(".cm-content")?.textContent).toContain('"row":0'));
    fireEvent.click(assertNamedControl(paged.getByTestId("files-readonly-goto"), "button"));
    expect(paged.getByTestId("files-readonly-goto").getAttribute("aria-expanded")).toBe("true");
  });

  it("mounts language grammars, renders language labels, and applies project lineWidth in ChatContentPage", async () => {
    void saveEditorLineLayout("pretty");
    const py = `def f(a, b): return {${Array.from({ length: 16 }, (_, i) => `"key_${i}": ${i}`).join(", ")}}\n`;
    const client = {
      getProjectSourceFormatSettings: async () => ({
        config_names: ["pyproject.toml"],
        sources: [{ tool: "ruff", configs: ["pyproject.toml"], settings: { line_width: 80 } }],
      }),
    } as never;
    const view = render(() => (
      <ChatContentPage
        projectId="proj"
        client={client}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "m1",
          toolCallId: "c1",
          pane: "raw",
          title: "app.py",
          path: "src/app.py",
          content: { kind: "inline", text: py },
        }}
      />
    ));
    await waitFor(() => {
      expect(view.container.querySelectorAll(".pw-pretty-break").length).toBeGreaterThan(0);
    });
    expect(view.container.querySelector(".den-files-editor__status-language")?.textContent).toContain("Python");

    const mdView = render(() => (
      <ChatContentPage
        projectId="proj"
        client={null}
        document={{
          kind: "tool",
          sessionId: "session",
          messageId: "m2",
          toolCallId: "c2",
          pane: "raw",
          title: "notes.md",
          path: "docs/notes.md",
          content: {
            kind: "inline",
            text: `\`\`\`python\nmsg = "${"a".repeat(120)}, ${"b".repeat(120)}"\n\`\`\`\n`,
          },
        }}
      />
    ));
    await waitFor(() => {
      expect(mdView.container.querySelector(".den-files-editor__status-language")?.textContent).toContain("Markdown");
    });
    expect(mdView.container.querySelectorAll(".pw-pretty-break").length).toBe(0);
  });
});
