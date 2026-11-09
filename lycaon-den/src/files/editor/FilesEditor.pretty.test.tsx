// @vitest-environment jsdom
import {
  deleteCharBackward,
  deleteCharForward,
  indentLess,
  indentMore,
  insertNewlineAndIndent,
} from "@codemirror/commands";
import { EditorSelection, EditorState, Transaction } from "@codemirror/state";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { stepEditorHistory } from "../../components/source/editor/editor-history.ts";
import { prettyNativeStateField } from "../../components/source/pretty/pretty-native-layout.ts";
import {
  gotoLineController,
  openGotoLine,
  resetGotoLineForTests,
  submitGotoLine,
} from "../../find/goto-line-controller.ts";
import { stubClient } from "../../test/client-fixture.ts";
import "../../test/document-outbox-fixture.ts";
import {
  PROJECT,
  editorDocumentFixtureFor,
  editorProps,
  loadedBuffer,
} from "../components/project-files-view-test-harness.ts";
import {
  configureEditorDocuments,
  resolveEditorDocument,
} from "../documents/editor-document.ts";
import {
  projectFilesState,
  type FileBuffer,
} from "../documents/files-buffer-state.ts";
import {
  applyFilesBufferEditorDocument,
  closeFilesBuffer,
  filesBufferText,
} from "../documents/project-files-buffers.ts";
import { FilesEditor } from "./FilesEditor.tsx";
import { getFilesEditorView, getFilesEditorViewDoc } from "./files-editor-host.ts";
import { applyBufferReveal } from "./files-editor-reveal.ts";
import { setFilesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import {
  copyTextToClipboard,
  setupFilesEditorTests,
} from "./files-editor-test-harness.ts";
import {
  anchorSecretSelection,
  secretActionRange,
} from "./files-secret-actions.ts";
import {
  invokePrettyToggleForTab,
  isPrettyDisplayEnabled,
  resetPrettyDisplayStateForTests,
} from "./pretty-display-state.ts";

async function openAndAttachDocumentReplica(
  buf: FileBuffer,
): Promise<NonNullable<Awaited<ReturnType<typeof resolveEditorDocument>>>> {
  const fixture = editorDocumentFixtureFor(PROJECT, buf.rootId, buf.path);
  const client = stubClient({
    openEditorDocument: async () => fixture.snapshot(),
    syncEditorDocument: fixture.sync,
    submitEditorDocumentUpdate: fixture.submit,
  });
  configureEditorDocuments(
    PROJECT,
    () => client,
    () => undefined,
    {
      all: () => Object.values(projectFilesState(PROJECT).byKey),
      get: (key: string) => projectFilesState(PROJECT).byKey[key],
    },
  );
  const docState = await resolveEditorDocument(PROJECT, buf);
  expect(docState).not.toBeNull();
  applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);
  return docState!;
}

describe("FilesEditor Source-Native Editable Pretty Integration (Decisions 004 & 005, FMT04, FMT05)", () => {
  setupFilesEditorTests();

  afterEach(() => {
    resetPrettyDisplayStateForTests();
    resetGotoLineForTests();
  });

  it("toggles Pretty from the status bar and surface command without mutating raw bytes, dirty state, or editor identity (FMT-AC-06, FMT-AC-09)", async () => {
    const rawJsonl =
      '{"id":1,"msg":"hello\\nworld","nested":{"a":1,"b":2}}\n{"id":2,"arr":[10,20]}\n';
    const buf = loadedBuffer({
      path: "events.jsonl",
      content: rawJsonl,
    });
    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);

    const view = getFilesEditorView(PROJECT, buf.key)!;
    expect(view).toBeDefined();
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(getFilesEditorViewDoc(PROJECT, buf.key)).toBe(rawJsonl);

    const toggleBtn = screen.getByTestId("files-editor-pretty-toggle");
    expect(toggleBtn.getAttribute("aria-pressed")).toBe("false");
    expect(
      document
        .querySelector(".den-files-editor__status")
        ?.getAttribute("data-editor-identity"),
    ).toBe("editing");

    // Toggle Pretty ON via status bar button
    fireEvent.click(toggleBtn);
    await waitFor(() => {
      expect(toggleBtn.getAttribute("aria-pressed")).toBe("true");
    });
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    // Raw document text and line count are completely untouched; layout is supplied by native decorations.
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(getFilesEditorViewDoc(PROJECT, buf.key)).toBe(rawJsonl);
    expect(view.state.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBeGreaterThan(4);
    expect(view.dom.querySelectorAll(".pw-virtual-indent").length).toBeGreaterThan(3);
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
    expect(view.dom.querySelectorAll(".cm-panels")).toHaveLength(0);
    expect(
      document
        .querySelector(".den-files-editor__status")
        ?.getAttribute("data-editor-identity"),
    ).toBe("editing");

    // Toggle Pretty OFF via command handler (`invokePrettyToggleForTab`)
    expect(invokePrettyToggleForTab(PROJECT, buf.key)).toBe(true);
    await waitFor(() => {
      expect(toggleBtn.getAttribute("aria-pressed")).toBe("false");
    });
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(view.dom.querySelectorAll(".pw-virtual-break")).toHaveLength(0);
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
  });

  it("edits raw source directly in Pretty mode (typing, Backspace/Delete on commas/braces, Enter, Tab/Shift+Tab, multi-cursor, malformed JSON) without synthetic whitespace or recovery dialogs (FMT-AC-10, FMT-AC-16, FMT-AC-17)", async () => {
    const rawJson = '{"service":"gateway","enabled":true,"ports":[8080,8443]}';
    const buf = loadedBuffer({
      path: "config.json",
      content: rawJson,
    });
    const docState = await openAndAttachDocumentReplica(buf);
    const replica = docState.replica;

    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);

    const view = getFilesEditorView(PROJECT, buf.key)!;
    const toggleBtn = screen.getByTestId("files-editor-pretty-toggle");
    fireEvent.click(toggleBtn);
    await waitFor(() => {
      expect(toggleBtn.getAttribute("aria-pressed")).toBe("true");
    });
    expect(replica.text).toBe(rawJson);

    // 1. Delete comma after `"gateway"` via Backspace (deleteCharBackward):
    // Pretty remains enabled, raw text becomes temporarily malformed JSON, and zero recovery UI appears.
    const commaIdx = view.state.doc.toString().indexOf(",");
    view.dispatch({
      selection: { anchor: commaIdx + 1, head: commaIdx + 1 },
    });
    expect(deleteCharBackward(view)).toBe(true);
    expect(view.state.doc.toString()).toBe(
      '{"service":"gateway""enabled":true,"ports":[8080,8443]}',
    );
    expect(replica.text).toBe(view.state.doc.toString());
    expect(view.state.field(prettyNativeStateField).enabled).toBe(true);
    expect(view.state.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(screen.queryByTestId("pretty-recovery-dialog")).toBeNull();

    // 2. Undo restores the deleted comma via replica.stepHistory:
    expect(stepEditorHistory(view, "undo")).toBe(true);
    expect(view.state.doc.toString()).toBe(rawJson);
    expect(replica.text).toBe(rawJson);

    // 3. Delete comma via forward Delete (deleteCharForward):
    view.dispatch({
      selection: { anchor: commaIdx, head: commaIdx },
    });
    expect(deleteCharForward(view)).toBe(true);
    expect(view.state.doc.toString()).toBe(
      '{"service":"gateway""enabled":true,"ports":[8080,8443]}',
    );
    expect(stepEditorHistory(view, "undo")).toBe(true);
    expect(view.state.doc.toString()).toBe(rawJson);

    // 4. Press Enter (insertNewlineAndIndent) and Tab/Shift+Tab (indentMore/indentLess):
    view.dispatch({
      selection: { anchor: commaIdx + 1, head: commaIdx + 1 },
    });
    expect(insertNewlineAndIndent(view)).toBe(true);
    expect(view.state.doc.lines).toBe(2);
    expect(indentMore(view)).toBe(true);
    expect(indentLess(view)).toBe(true);
    expect(replica.text).toBe(view.state.doc.toString());

    // 5. Multi-cursor edit on port numbers `8080` and `8443` directly updates both in raw source:
    const curText = view.state.doc.toString();
    const p1 = curText.indexOf("8080");
    const p2 = curText.indexOf("8443");
    view.dispatch({
      selection: EditorSelection.create([
        EditorSelection.range(p1, p1 + 4),
        EditorSelection.range(p2, p2 + 4),
      ]),
    });
    view.dispatch(
      view.state.changeByRange((range) => ({
        changes: { from: range.from, to: range.to, insert: "9000" },
        range: EditorSelection.cursor(range.from + 4),
      })),
    );
    expect(view.state.doc.toString()).toContain("[9000,9000]");
    expect(replica.text).toContain("[9000,9000]");
  });

  it("keeps transaction construction pure, respects later filter rejections, and synchronizes remote replica updates (FMT-AC-12, FMT-AC-17)", async () => {
    const rawJson = '{"count":1,"status":"ready"}';
    const buf = loadedBuffer({
      path: "state.json",
      content: rawJson,
    });
    const docState = await openAndAttachDocumentReplica(buf);
    const replica = docState.replica;

    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);
    const view = getFilesEditorView(PROJECT, buf.key)!;

    invokePrettyToggleForTab(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);

    // 1. Constructing a transaction with `view.state.update(...)` without dispatching must not mutate `replica`:
    const undispatched = view.state.update({
      changes: { from: 9, to: 10, insert: "999" },
      annotations: Transaction.userEvent.of("input.type"),
    });
    expect(undispatched.state.doc.toString()).toContain("999");
    expect(view.state.doc.toString()).toBe(rawJson);
    expect(replica.text).toBe(rawJson);

    // 2. Remote replica update (`replica.applyRemote`) updates `view.state.doc` and recalculates native Pretty decorations:
    const remoteJson = '{"count":2,"status":"synced","extra":{"ok":true}}';
    replica.applyRemote(
      replica.version + 1,
      [{ start_utf16: 0, end_utf16: rawJson.length, text: remoteJson }],
      "remote-sha",
    );
    await waitFor(() => {
      expect(view.state.doc.toString()).toBe(remoteJson);
    });
    expect(view.state.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBeGreaterThan(3);
  });

  it("preserves raw source coordinates for Go to line, buffer reveal, SecretScreen spans, Copy contents, common-blob formats, and tab disposal (FMT-AC-13, FMT-AC-15, FMT-AC-18)", async () => {
    const rawJsonl =
      '{"line":1,"secret":"sk-live-123456"}\n{"line":2,"target":"here","nested":{"x":10}}\n';
    const buf = loadedBuffer({
      path: "audit.jsonl",
      content: rawJsonl,
    });
    setFilesEditorSecretScreen(buf.key, {
      status: "findings",
      screened_bytes: rawJsonl.length,
      findings: [
        {
          rule_id: "api-key",
          title: "API Key",
          line: 1,
          start_rune: rawJsonl.indexOf("sk-live-123456"),
          end_rune: rawJsonl.indexOf("sk-live-123456") + 14,
          redacted_preview: "sk-****",
        },
      ],
    });

    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);
    const view = getFilesEditorView(PROJECT, buf.key)!;

    // Enable Pretty
    invokePrettyToggleForTab(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBeGreaterThan(3);

    // 1. Go to line 2, column 5 lands on raw line 2, column 5:
    openGotoLine(view);
    expect(gotoLineController.isOpen()).toBe(true);
    gotoLineController.setInput("2:5");
    expect(submitGotoLine()).toBe(true);
    const line2 = view.state.doc.line(2);
    expect(view.state.selection.main.head).toBe(line2.from + 4);

    // 2. Buffer reveal uses raw line/col directly:
    buf.reveal = { line: 2, col: 10 };
    applyBufferReveal(view, buf);
    expect(view.state.selection.main.head).toBe(line2.from + 9);

    // 3. SecretScreen range & selection operate on raw UTF-16 offsets:
    const finding = {
      rule_id: "api-key",
      title: "API Key",
      line: 1,
      start_rune: rawJsonl.indexOf("sk-live-123456"),
      end_rune: rawJsonl.indexOf("sk-live-123456") + 14,
      redacted_preview: "sk-****",
    };
    const range = secretActionRange(view.state.doc, finding);
    expect(range).toEqual({
      from: rawJsonl.indexOf("sk-live-123456"),
      to: rawJsonl.indexOf("sk-live-123456") + 14,
    });
    anchorSecretSelection(view, range!.from, range!.to);
    expect(
      view.state.doc.sliceString(
        view.state.selection.main.from,
        view.state.selection.main.to,
      ),
    ).toBe("sk-live-123456");

    // 4. Copy contents copies exact raw text without generated decoration whitespace:
    await copyTextToClipboard(view.state.doc.toString());
    expect(filesBufferText(buf)).toBe(rawJsonl);

    // 5. Closing the buffer disposes the tab's Pretty state:
    closeFilesBuffer(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(false);
  });
});
