// @vitest-environment jsdom
import { setupFilesEditorTests } from "./files-editor-test-harness.ts";
import "../../test/document-outbox-fixture.ts";
import {
  PROJECT,
  editorDocumentFixtureFor,
  editorProps,
  loadedBuffer,
} from "../components/project-files-view-test-harness.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { undo, redo } from "@codemirror/commands";
import { FilesEditor } from "./FilesEditor.tsx";
import {
  getFilesEditorAuthoritativeViewDoc,
  getFilesEditorPrettyAdapter,
  getFilesEditorView,
  getFilesEditorViewDoc,
} from "./files-editor-host.ts";
import {
  applyFilesBufferEditorDocument,
  closeFilesBuffer,
  filesBufferText,
} from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import {
  evictEditorDocument,
  resolveEditorDocument,
} from "../documents/editor-document.ts";
import { stubClient } from "../../test/client-fixture.ts";
import {
  isPrettyDisplayEnabled,
  invokePrettyToggleForTab,
  resetPrettyDisplayStateForTests,
} from "./pretty-display-state.ts";
import {
  gotoLineController,
  openGotoLine,
  resetGotoLineForTests,
  submitGotoLine,
} from "../../find/goto-line-controller.ts";
import { applyBufferReveal } from "./files-editor-reveal.ts";
import { setFilesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import {
  anchorSecretSelection,
  secretActionRange,
} from "./files-secret-actions.ts";
import { createFileConfirmations } from "../commands/file-confirmations.ts";
import {
  clearAllPrettyDocumentRecovery,
  createPrettyDocumentRecoveryController,
  hasUnpersistedPrettyRecoveryForDocument,
  type PrettyRecoveryStorageLike,
} from "../../components/source/pretty/pretty-replica-recovery.ts";
import { summarizePreviewText } from "../../components/source/pretty/pretty-replica-recovery-ui.ts";

describe("FilesEditor Pretty display integration", () => {
  setupFilesEditorTests();

  afterEach(() => {
    resetPrettyDisplayStateForTests();
    resetGotoLineForTests();
    clearAllPrettyDocumentRecovery();
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
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(rawJsonl);

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
    // Display doc is multi-line formatted projection, while authoritative doc stays raw
    expect(view.state.doc.lines).toBeGreaterThan(3);
    expect(view.state.doc.toString()).toContain('"msg": "hello\\nworld"');
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(rawJsonl);
    expect(getFilesEditorViewDoc(PROJECT, buf.key)).toBe(rawJsonl);
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
    expect(view.dom.querySelectorAll(".cm-panels")).toHaveLength(0);
    expect(
      document
        .querySelector(".den-files-editor__status")
        ?.getAttribute("data-editor-identity"),
    ).toBe("editing");

    // Toggle Pretty OFF via surface command (Alt+P / View menu command path)
    expect(invokePrettyToggleForTab(PROJECT, buf.key)).toBe(true);
    await waitFor(() => {
      expect(toggleBtn.getAttribute("aria-pressed")).toBe("false");
    });
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(rawJsonl);
    expect(projectFilesState(PROJECT).byKey[buf.key]!.dirty).toBe(false);
  });

  it("maps edits in Pretty mode through DocumentReplica, updates buffer dirty state, preserves undo/redo across mode switches, and saves raw bytes (FMT-AC-10, FMT-AC-11, FMT-AC-12)", async () => {
    const rawJsonl = '{"id":1,"name":"alpha"}\n{"id":2,"name":"bravo"}\n';
    const buf = loadedBuffer({
      path: "records.jsonl",
      content: rawJsonl,
    });
    const fixture = editorDocumentFixtureFor(PROJECT, "r1", "records.jsonl");
    const client = stubClient({
      openEditorDocument: async () => fixture.snapshot(),
      syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit,
    });
    const docState = await resolveEditorDocument(
      PROJECT,
      () => client,
      "r1",
      "records.jsonl",
    );
    expect(docState).not.toBeNull();
    applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);

    const currentBuf = () => projectFilesState(PROJECT).byKey[buf.key]!;
    render(() => <FilesEditor {...editorProps(currentBuf())} />);

    const view = getFilesEditorView(PROJECT, buf.key)!;
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    const adapter = getFilesEditorPrettyAdapter(PROJECT, buf.key);
    expect(adapter).not.toBeNull();
    expect(view.state.doc.toString()).toContain('"name": "alpha"');

    // Replace "alpha" with "charlie" in Pretty mode
    const alphaIdx = view.state.doc.toString().indexOf("alpha");
    expect(alphaIdx).toBeGreaterThan(0);
    view.dispatch({
      changes: { from: alphaIdx, to: alphaIdx + 5, insert: "charlie" },
      userEvent: "input.type",
    });

    const expectedRaw = '{"id":1,"name":"charlie"}\n{"id":2,"name":"bravo"}\n';
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(expectedRaw);
    expect(getFilesEditorViewDoc(PROJECT, buf.key)).toBe(expectedRaw);
    await waitFor(() => {
      expect(currentBuf().dirty).toBe(true);
      expect(filesBufferText(currentBuf())).toBe(expectedRaw);
    });

    // Toggle Pretty OFF and verify raw view + undo/redo across mode switches
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(false);
    });
    expect(view.state.doc.toString()).toBe(expectedRaw);

    expect(undo(view)).toBe(true);
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(rawJsonl);

    // Toggle Pretty back ON and redo
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });
    expect(redo(view)).toBe(true);
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(expectedRaw);
    expect(view.state.doc.toString()).toContain('"name": "charlie"');
  });

  it("displays original 1-based source line numbers in the gutter and maps Go to Line, reveal, and cursor reporting by source coordinates (FMT-AC-13)", async () => {
    const rawJsonl = '{"a":1,"b":2}\n{"c":3,"d":4}\n';
    const buf = loadedBuffer({
      path: "coords.jsonl",
      content: rawJsonl,
    });
    const fixture = editorDocumentFixtureFor(PROJECT, "r1", "coords.jsonl");
    const client = stubClient({
      openEditorDocument: async () => fixture.snapshot(),
      syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit,
    });
    const docState = await resolveEditorDocument(
      PROJECT,
      () => client,
      "r1",
      "coords.jsonl",
    );
    applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);

    const props = editorProps(projectFilesState(PROJECT).byKey[buf.key]!);
    render(() => <FilesEditor {...props} />);

    const view = getFilesEditorView(PROJECT, buf.key)!;
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    const adapter = getFilesEditorPrettyAdapter(PROJECT, buf.key)!;
    expect(adapter).not.toBeNull();
    // Line 1 of display is start of source line 1 -> "1"; continuation lines of record 1 -> ""
    expect(adapter.getLineGutterLabel(1)).toBe("1");
    expect(adapter.getLineGutterLabel(2)).toBe("");
    const line2DispOffset = adapter.mapSourceLineColToDisplay(2, 1);
    const line2DispLine = view.state.doc.lineAt(line2DispOffset).number;
    expect(line2DispLine).toBeGreaterThan(2);
    expect(adapter.getLineGutterLabel(line2DispLine)).toBe("2");
    expect(adapter.getLineGutterLabel(line2DispLine + 1)).toBe("");

    // Go to Line uses source line count (3 lines including trailing newline) and navigates by source line:col
    openGotoLine(view);
    expect(gotoLineController.maxLine()).toBe(3);
    gotoLineController.setInput("2:8");
    expect(submitGotoLine()).toBe(true);

    const expectedDispPos = adapter.mapSourceLineColToDisplay(2, 8);
    expect(view.state.selection.main.head).toBe(expectedDispPos);
    expect(props.onCursor).toHaveBeenLastCalledWith({ line: 2, col: 8 });

    // applyBufferReveal maps source revealLine/revealColumn to display coordinates
    const revealBuf = {
      ...projectFilesState(PROJECT).byKey[buf.key]!,
      revealNonce: 99,
      revealLine: 2,
      revealColumn: 2,
    };
    const applied = applyBufferReveal(view, revealBuf, 0, {
      projectId: PROJECT,
      onCursor: props.onCursor,
    });
    expect(applied).toBe(99);
    expect(view.state.selection.main.head).toBe(
      adapter.mapSourceLineColToDisplay(2, 2),
    );
  });

  it("projects SecretScreen spans in Pretty mode and maps secret selections back to raw source offsets", async () => {
    const rawJsonl = '{"key":"AKIAQYJK5TXV4NZR7SGB"}\n';
    const secretStart = rawJsonl.indexOf("AKIA");
    const secretEnd = secretStart + 20;
    const buf = loadedBuffer({
      path: "secrets.jsonl",
      content: rawJsonl,
    });
    const fixture = editorDocumentFixtureFor(PROJECT, "r1", "secrets.jsonl");
    const client = stubClient({
      openEditorDocument: async () => fixture.snapshot(),
      syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit,
    });
    const docState = await resolveEditorDocument(
      PROJECT,
      () => client,
      "r1",
      "secrets.jsonl",
    );
    applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);

    render(() => (
      <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[buf.key]!)} />
    ));
    const view = getFilesEditorView(PROJECT, buf.key)!;

    const screenData = {
      truncated: false,
      spans: [
        {
          start: secretStart,
          end: secretEnd,
          state: "detected" as const,
          rule_id: "aws",
        },
      ],
    };
    setFilesEditorSecretScreen(view, screenData);

    // Toggle Pretty ON: secret span is projected to the formatted display position
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });
    const dispSecretStart = view.state.doc.toString().indexOf("AKIA");
    expect(dispSecretStart).toBeGreaterThan(secretStart);

    view.dispatch({ selection: { anchor: dispSecretStart + 2 } });
    const range = secretActionRange(view);
    expect(range).toMatchObject({
      from: dispSecretStart,
      to: dispSecretStart + 20,
    });

    // anchorSecretSelection maps display selection back to raw rune offsets [secretStart, secretEnd]
    const previewEditorSecretMark = vi.fn(async () => ({
      eligible: true,
      start: secretStart,
      end: secretEnd,
      rune_length: 20,
      byte_length: 20,
    }));
    const secretClient = stubClient({ previewEditorSecretMark });
    const anchored = await anchorSecretSelection(
      secretClient,
      {
        projectId: PROJECT,
        documentId: docState!.documentId,
        view,
        sync: async () => ({ revision: docState!.revision, text: rawJsonl }),
      },
      { from: dispSecretStart, to: dispSecretStart + 20 },
    );
    expect(anchored).toMatchObject({
      revision: docState!.revision,
      range: { start: secretStart, end: secretEnd },
    });
    expect(previewEditorSecretMark).toHaveBeenCalledWith(
      PROJECT,
      docState!.documentId,
      {
        revision: docState!.revision,
        start: secretStart,
        end: secretEnd,
        trim: true,
      },
    );
  });

  it("guards tab close, reload, and document eviction when memory-only Pretty recovery exists, and cleans up Pretty state on close (FMT-AC-15)", async () => {
    const rawJsonl = '{"id":1,"v":"one"}\n';
    const buf = loadedBuffer({
      path: "guarded.jsonl",
      content: rawJsonl,
    });
    const fixture = editorDocumentFixtureFor(PROJECT, "r1", "guarded.jsonl");
    const client = stubClient({
      openEditorDocument: async () => fixture.snapshot(),
      syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit,
    });
    const docState = await resolveEditorDocument(
      PROJECT,
      () => client,
      "r1",
      "guarded.jsonl",
    );
    applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);
    render(() => (
      <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[buf.key]!)} />
    ));

    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    // Simulate a failing recovery storage so a retained record is held in memory only
    const failingStorage: PrettyRecoveryStorageLike = {
      getItem: () => null,
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
      removeItem: () => {},
    };
    const controller = createPrettyDocumentRecoveryController({
      documentId: docState!.documentId,
      incarnation: 1,
      storage: failingStorage,
    });
    controller.captureRejectedEdit({
      kind: "conflict",
      reason: "multi-cursor-raw-conflict",
      rawRevision: 1,
      rawBaseText: rawJsonl,
      displayText: '{\n  "id": 1,\n  "v": "two"\n}\n',
      displaySelection: { mainIndex: 0, ranges: [{ anchor: 0, head: 0 }] },
      intentions: [
        {
          index: 0,
          displayFrom: 0,
          displayTo: 0,
          rawFrom: 8,
          rawTo: 17,
          insertedText: '"v":"two"',
          mappedStatus: "ok",
        },
      ],
    });

    expect(hasUnpersistedPrettyRecoveryForDocument(docState!.documentId)).toBe(
      true,
    );

    // Eviction is blocked while unpersisted recovery exists
    expect(await evictEditorDocument(docState!.documentId)).toBe(false);

    // Close confirmation prompts instead of closing silently even when buffer.dirty is false
    const performCloseTab = vi.fn();
    const confirmations = createFileConfirmations({
      projectId: () => PROJECT,
      activeBuffer: () => projectFilesState(PROJECT).byKey[buf.key],
      performReload: vi.fn(async () => {}),
      performDiscard: vi.fn(async () => {}),
      performCloseTab,
    });
    confirmations.requestCloseTab(buf.key);
    expect(performCloseTab).not.toHaveBeenCalled();
    expect(confirmations.confirming()).toEqual({
      kind: "close",
      key: buf.key,
    });

    // Confirmed close clears Pretty display state and recovery state for the tab
    closeFilesBuffer(PROJECT, buf.key);
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(false);
    expect(hasUnpersistedPrettyRecoveryForDocument(docState!.documentId)).toBe(
      false,
    );
  });

  it("bounds recovery preview labels, returns focus to the editor on Escape, and reports accurate async clipboard status", async () => {
    // 1. Bounded single-line preview summary helper
    const longMultiline = "first line\nsecond line with lots of extra characters";
    const summary = summarizePreviewText(longMultiline, 24);
    expect(summary).not.toContain("\n");
    expect(summary).toContain("↵");
    expect(summary).toContain(`(${longMultiline.length} chars)`);

    // 2. Render a FilesEditor with a conflict recovery tooltip and test Escape + async clipboard
    const rawJsonl = '{"a":1,"b":2}\n';
    const buf = loadedBuffer({
      path: "recovery-ui.jsonl",
      content: rawJsonl,
    });
    const fixture = editorDocumentFixtureFor(PROJECT, "r1", "recovery-ui.jsonl");
    const client = stubClient({
      openEditorDocument: async () => fixture.snapshot(),
      syncEditorDocument: fixture.sync,
      submitEditorDocumentUpdate: fixture.submit,
    });
    const docState = await resolveEditorDocument(
      PROJECT,
      () => client,
      "r1",
      "recovery-ui.jsonl",
    );
    applyFilesBufferEditorDocument(PROJECT, buf.key, docState!);
    render(() => (
      <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[buf.key]!)} />
    ));

    const view = getFilesEditorView(PROJECT, buf.key)!;
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    const adapter = getFilesEditorPrettyAdapter(PROJECT, buf.key)!;
    adapter.recoveryController.captureRejectedEdit({
      kind: "conflict",
      reason: "multi-cursor-raw-conflict",
      rawRevision: 1,
      rawBaseText: rawJsonl,
      displayText: "{\n  \"a\": 100,\n  \"b\": 200\n}\n",
      displaySelection: { mainIndex: 0, ranges: [{ anchor: 0, head: 0 }] },
      intentions: [
        {
          index: 0,
          displayFrom: 0,
          displayTo: 5,
          rawFrom: 1,
          rawTo: 6,
          insertedText: '"a":100,\n"extra":"very_long_multiline_value_for_button"',
          mappedStatus: "ok",
        },
      ],
    });
    adapter.setRecoveryFeedback("Ready");

    const tooltip = await waitFor(() => {
      const el = view.dom.querySelector(
        '[data-testid="pretty-recovery-tooltip"]',
      ) as HTMLElement | null;
      expect(el).not.toBeNull();
      return el!;
    });

    const applyBtn = tooltip.querySelector(
      '[data-testid="pretty-recovery-apply-0"]',
    ) as HTMLButtonElement;
    expect(applyBtn.textContent).toContain("Insert #0");
    expect(applyBtn.textContent).not.toContain("\n");
    expect(applyBtn.textContent).toContain("↵");

    // Async clipboard failure path falls back to manual copy instruction
    const origClipboard = navigator.clipboard;
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn(async () => {
          throw new Error("Denied");
        }),
      },
    });
    try {
      const copyBtn = tooltip.querySelector(
        '[data-testid="pretty-recovery-copy"]',
      ) as HTMLButtonElement;
      fireEvent.click(copyBtn);
      await waitFor(() => {
        const feedback = view.dom.querySelector(
          '[data-testid="pretty-recovery-feedback"]',
        );
        expect(feedback?.textContent).toContain(
          "Selected retained edit in text box",
        );
      });

      // Async clipboard success path reports copied to clipboard
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: {
          writeText: vi.fn(async () => undefined),
        },
      });
      const copyBtn2 = view.dom.querySelector(
        '[data-testid="pretty-recovery-copy"]',
      ) as HTMLButtonElement;
      fireEvent.click(copyBtn2);
      await waitFor(() => {
        const feedback = view.dom.querySelector(
          '[data-testid="pretty-recovery-feedback"]',
        );
        expect(feedback?.textContent).toContain(
          "Copied retained edit to clipboard",
        );
      });
    } finally {
      Object.defineProperty(navigator, "clipboard", {
        configurable: true,
        value: origClipboard,
      });
    }

    // Escape inside recovery tooltip returns focus to view.contentDOM
    const focusSpy = vi.spyOn(view.contentDOM, "focus");
    const activeTooltip = view.dom.querySelector(
      '[data-testid="pretty-recovery-tooltip"]',
    ) as HTMLElement;
    fireEvent.keyDown(activeTooltip, { key: "Escape" });
    expect(focusSpy).toHaveBeenCalled();
  });
});
