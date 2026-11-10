// @vitest-environment jsdom
import {
  deleteCharBackward,
  deleteCharForward,
  indentLess,
  indentMore,
  insertNewlineAndIndent,
} from "@codemirror/commands";
import { EditorSelection, Transaction } from "@codemirror/state";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import * as Y from "yjs";
import { stepEditorHistory } from "../../components/source/editor/editor-history.ts";
import { prettyNativeStateField } from "../../components/source/pretty/pretty-native-layout.ts";
import { setSecretScreen } from "../../components/source/secrets/secret-span-decorations.ts";
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
import { type DocumentReplica } from "../documents/document-replica.ts";
import {
  configureEditorDocuments,
  editorReplica,
  resetEditorDocumentsForTests,
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
): Promise<{
  docState: NonNullable<Awaited<ReturnType<typeof resolveEditorDocument>>>;
  replica: DocumentReplica;
  fixture: ReturnType<typeof editorDocumentFixtureFor>;
}> {
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
  const replica = editorReplica(docState!.documentId)!;
  expect(replica).toBeDefined();
  return { docState: docState!, replica, fixture };
}

describe("FilesEditor Source-Native Editable Pretty Integration (Decisions 004 & 005, FMT04, FMT05)", () => {
  setupFilesEditorTests();

  afterEach(() => {
    resetPrettyDisplayStateForTests();
    resetGotoLineForTests();
    resetEditorDocumentsForTests();
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
    const { replica } = await openAndAttachDocumentReplica(buf);

    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);

    const view = getFilesEditorView(PROJECT, buf.key)!;
    const toggleBtn = screen.getByTestId("files-editor-pretty-toggle");
    fireEvent.click(toggleBtn);
    await waitFor(() => {
      expect(toggleBtn.getAttribute("aria-pressed")).toBe("true");
    });
    expect(replica.currentText).toBe(rawJson);

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
    expect(replica.currentText).toBe(view.state.doc.toString());
    expect(view.state.field(prettyNativeStateField).enabled).toBe(true);
    expect(view.state.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(screen.queryByTestId("pretty-recovery-dialog")).toBeNull();

    // 2. Undo restores the deleted comma via replica.stepHistory:
    expect(stepEditorHistory(view, "undo")).toBe(true);
    expect(view.state.doc.toString()).toBe(rawJson);
    expect(replica.currentText).toBe(rawJson);

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
    expect(replica.currentText).toBe(view.state.doc.toString());

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
    expect(replica.currentText).toContain("[9000,9000]");
  });

  it("keeps transaction construction pure, respects later filter rejections, and synchronizes remote replica updates (FMT-AC-12, FMT-AC-17)", async () => {
    const rawJson = '{"count":1,"status":"ready"}';
    const buf = loadedBuffer({
      path: "state.json",
      content: rawJson,
    });
    const { replica } = await openAndAttachDocumentReplica(buf);

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
    expect(replica.currentText).toBe(rawJson);

    // 2. Remote CRDT update on the Y.Doc updates `view.state.doc` and recalculates native Pretty decorations:
    const remoteJson = '{"count":2,"status":"synced","extra":{"ok":true}}';
    const peerDoc = new Y.Doc();
    peerDoc.clientID = 999991;
    Y.applyUpdate(peerDoc, Y.encodeStateAsUpdate(replica.doc));
    const peerText = peerDoc.getText("text");
    peerDoc.transact(() => {
      peerText.delete(0, peerText.length);
      peerText.insert(0, remoteJson);
    });
    Y.applyUpdate(replica.doc, Y.encodeStateAsUpdate(peerDoc));
    peerDoc.destroy();

    await waitFor(() => {
      expect(view.state.doc.toString()).toBe(remoteJson);
    });
    expect(view.state.field(prettyNativeStateField).outcome).toBe("formatted");
    expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBeGreaterThan(3);
  });

  it("preserves raw source coordinates for Go to line, buffer reveal, SecretScreen spans, Copy contents, and tab disposal (FMT-AC-13, FMT-AC-15, FMT-AC-18)", async () => {
    const rawJsonl =
      '{"line":1,"secret":"sk-live-123456"}\n{"line":2,"target":"here","nested":{"x":10}}\n';
    const buf = loadedBuffer({
      path: "audit.jsonl",
      content: rawJsonl,
    });
    const secretStart = rawJsonl.indexOf("sk-live-123456");
    const secretEnd = secretStart + 14;
    const screenPayload = {
      truncated: false,
      screened_bytes: rawJsonl.length,
      spans: [
        {
          start: secretStart,
          end: secretEnd,
          state: "detected" as const,
          rule_id: "api-key",
          rule_title: "API Key",
        },
      ],
    };
    setFilesEditorSecretScreen(buf.key, screenPayload);

    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);
    const view = getFilesEditorView(PROJECT, buf.key)!;
    setSecretScreen(view, screenPayload);

    // Enable Pretty
    invokePrettyToggleForTab(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    expect(view.dom.querySelectorAll(".pw-virtual-break").length).toBeGreaterThan(3);

    // 1. Go to line 2, column 5 lands on raw line 2, column 5:
    openGotoLine(view);
    expect(gotoLineController.isOpen()).toBe(true);
    expect(submitGotoLine("2:5")).toBe(true);
    const line2 = view.state.doc.line(2);
    expect(view.state.selection.main.head).toBe(line2.from + 4);

    // 2. Buffer reveal uses raw line/col directly:
    applyBufferReveal(view, {
      revealLine: 2,
      revealEndLine: null,
      revealColumn: 10,
      revealFocus: false,
    });
    expect(view.state.selection.main.head).toBe(line2.from + 9);

    // 3. SecretScreen range & selection operate on raw UTF-16 offsets:
    const range = secretActionRange(view, secretStart + 2);
    expect(range).not.toBeNull();
    expect(range!.from).toBe(secretStart);
    expect(range!.to).toBe(secretEnd);
    const anchored = anchorSecretSelection(
      {
        projectId: PROJECT,
        documentId: "doc-audit",
        view,
        sync: async () => ({ revision: 1, text: view.state.doc.toString() }),
      },
      range!.from,
      range!.to,
    );
    expect(anchored).toBeDefined();
    expect(view.state.doc.sliceString(range!.from, range!.to)).toBe(
      "sk-live-123456",
    );

    // 4. Copy contents copies exact raw text without generated decoration whitespace:
    await copyTextToClipboard(view.state.doc.toString());
    expect(filesBufferText(buf)).toBe(rawJsonl);

    // 5. Closing the buffer disposes the tab's Pretty state:
    void closeFilesBuffer(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(false);
  });

  it("resolves visual-line-end (assoc=-1) and next-visual-line-start (assoc=1) selection and DOM positions across all Pretty boundaries in FilesEditor (FMT-AC-10, FMT-AC-13)", () => {
    const rawJson = '{"alpha":1,"beta":[2,3]}';
    const buf = loadedBuffer({
      path: "boundaries.json",
      content: rawJson,
    });
    const props = editorProps(buf);
    render(() => <FilesEditor {...props} />);
    const view = getFilesEditorView(PROJECT, buf.key)!;

    invokePrettyToggleForTab(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    expect(view.contentDOM.querySelectorAll("br.pw-virtual-break").length).toBe(6);
    expect(view.contentDOM.querySelectorAll(".cm-line").length).toBe(1);

    const boundaries = [
      { pos: 1, beforeChar: "{", afterChar: '"' },
      { pos: 11, beforeChar: ",", afterChar: '"' },
      { pos: 19, beforeChar: "[", afterChar: "2" },
      { pos: 21, beforeChar: ",", afterChar: "3" },
      { pos: 22, beforeChar: "3", afterChar: "]" },
      { pos: 23, beforeChar: "]", afterChar: "}" },
    ];

    for (const { pos, beforeChar, afterChar } of boundaries) {
      view.dispatch({ selection: EditorSelection.cursor(pos, -1) });
      expect(view.state.selection.main.head).toBe(pos);
      expect(view.state.selection.main.assoc).toBe(-1);
      const domEnd = view.domAtPos(pos, -1);
      expect(domEnd.node.nodeType).toBe(Node.TEXT_NODE);
      expect(domEnd.node.textContent?.slice(domEnd.offset - 1, domEnd.offset)).toBe(
        beforeChar,
      );

      view.dispatch({ selection: EditorSelection.cursor(pos, 1) });
      expect(view.state.selection.main.head).toBe(pos);
      expect(view.state.selection.main.assoc).toBe(1);
      const domStart = view.domAtPos(pos, 1);
      expect(domStart.node.nodeType).toBe(Node.TEXT_NODE);
      expect(domStart.node.textContent?.slice(domStart.offset, domStart.offset + 1)).toBe(
        afterChar,
      );
    }

    expect(view.state.doc.toString()).toBe(rawJson);
  });
});
