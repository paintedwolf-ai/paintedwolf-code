// @vitest-environment jsdom
import {
  copyTextToClipboard,
  setupFilesEditorTests,
} from "./files-editor-test-harness.ts";
import "../../test/document-outbox-fixture.ts";
import {
  PROJECT,
  editorDocumentFixtureFor,
  editorProps,
  loadedBuffer,
} from "../components/project-files-view-test-harness.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { stepEditorHistory } from "../../components/source/editor/editor-history.ts";
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
import {
  projectFilesState,
  type FileBuffer,
} from "../documents/files-buffer-state.ts";
import {
  configureEditorDocuments,
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
  appendMultiCursorConflictToRecovery,
  configurePrettyRecoveryStorageForTests,
  getPrettyDocumentRecoveryState,
  hasUnpersistedPrettyRecoveryForDocument,
  resetPrettyDocumentRecoveryStoreForTests,
  type PrettyRecoveryStorage,
} from "../../components/source/pretty/pretty-replica-recovery.ts";
import { summarizePreviewText } from "../../components/source/pretty/pretty-replica-recovery-ui.ts";

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

describe("FilesEditor Pretty display integration", () => {
  setupFilesEditorTests();

  afterEach(() => {
    resetPrettyDisplayStateForTests();
    resetGotoLineForTests();
    resetPrettyDocumentRecoveryStoreForTests();
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
    await openAndAttachDocumentReplica(buf);

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

    expect(stepEditorHistory(view, "undo")).toBe(true);
    expect(view.state.doc.toString()).toBe(rawJsonl);
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(rawJsonl);

    // Toggle Pretty back ON and redo
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });
    expect(stepEditorHistory(view, "redo")).toBe(true);
    expect(getFilesEditorAuthoritativeViewDoc(view)).toBe(expectedRaw);
    expect(view.state.doc.toString()).toContain('"name": "charlie"');
  });

  it("displays original 1-based source line numbers in the gutter and maps Go to Line, reveal, and cursor reporting by source coordinates (FMT-AC-13)", async () => {
    const rawJsonl = '{"a":1,"b":2}\n{"c":3,"d":4}\n';
    const buf = loadedBuffer({
      path: "coords.jsonl",
      content: rawJsonl,
    });
    await openAndAttachDocumentReplica(buf);

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
    expect(adapter.getLineGutterLabel(1).label).toBe("1");
    expect(adapter.getLineGutterLabel(2).label).toBe("");
    const line2Mapped = adapter.mapSourceLineColToDisplay(2, 1);
    const line2DispLine = view.state.doc.lineAt(line2Mapped.offset).number;
    expect(line2DispLine).toBeGreaterThan(2);
    expect(adapter.getLineGutterLabel(line2DispLine).label).toBe("2");
    expect(adapter.getLineGutterLabel(line2DispLine + 1).label).toBe("");

    // Go to Line uses source line count (3 lines including trailing newline) and navigates by source line:col
    openGotoLine(view);
    expect(gotoLineController.state()?.target.lineCount).toBe(3);
    expect(submitGotoLine("2:8")).toBe(true);

    const expectedDispPos = adapter.mapSourceLineColToDisplay(2, 8).offset;
    expect(view.state.selection.main.head).toBe(expectedDispPos);
    expect(props.onCursor).toHaveBeenLastCalledWith(2, 8);

    // applyBufferReveal maps source revealLine/revealColumn to display coordinates
    applyBufferReveal(view, {
      revealLine: 2,
      revealColumn: 2,
      revealEndLine: undefined,
      revealFocus: false,
    });
    expect(view.state.selection.main.head).toBe(
      adapter.mapSourceLineColToDisplay(2, 2).offset,
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
    const docState = await openAndAttachDocumentReplica(buf);

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
    setFilesEditorSecretScreen(buf.key, screenData);

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
    const anchored = anchorSecretSelection(
      {
        projectId: PROJECT,
        documentId: docState.documentId,
        view,
        sync: async () => ({
          revision: docState.revision,
          text: rawJsonl,
          eol: "lf",
        }),
      },
      dispSecretStart,
      dispSecretStart + 20,
    );
    const preview = await anchored.preview(secretClient, true);
    expect(preview).toMatchObject({
      eligible: true,
      start: secretStart,
      end: secretEnd,
    });
    expect(previewEditorSecretMark).toHaveBeenCalledWith(
      PROJECT,
      docState.documentId,
      {
        revision: docState.revision,
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
    const docState = await openAndAttachDocumentReplica(buf);
    render(() => (
      <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[buf.key]!)} />
    ));

    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    // Simulate a failing recovery storage so a retained record is held in memory only
    const failingStorage: PrettyRecoveryStorage = {
      getItem: () => null,
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
      removeItem: () => {},
    };
    configurePrettyRecoveryStorageForTests(failingStorage);

    const adapter = getFilesEditorPrettyAdapter(PROJECT, buf.key)!;
    const recovery = getPrettyDocumentRecoveryState(
      adapter.replica.accepted.id,
      adapter.replica.incarnation,
    );
    appendMultiCursorConflictToRecovery(
      recovery,
      "runtime-1",
      recovery.incarnation,
      1,
      1,
      "fp-1",
      {
        reason: "overlapping-raw-ranges",
        sourceRevision: 1,
        intentions: [
          {
            order: 0,
            dispFrom: 0,
            dispTo: 5,
            rawFrom: 8,
            rawTo: 17,
            insert: '"v":"two"',
          },
        ],
      },
      [{ srcFrom: 0, srcTo: rawJsonl.length }],
    );

    expect(hasUnpersistedPrettyRecoveryForDocument(docState.documentId)).toBe(
      true,
    );

    // Eviction is blocked while unpersisted recovery exists
    expect(
      await evictEditorDocument(docState.documentId, { ifIdle: true }),
    ).toBe(false);
    await expect(evictEditorDocument(docState.documentId)).rejects.toThrow(
      "held in memory only",
    );

    // Close confirmation prompts instead of closing silently even when buffer.dirty is false
    const closeBufferSpy = vi.fn(() => null);
    const confirmations = createFileConfirmations({
      projectId: () => PROJECT,
      state: () => projectFilesState(PROJECT),
      closeBuffer: closeBufferSpy,
      closeBuffers: () => true,
      reloadBuffer: () => {},
      discardBuffer: async () => {},
      performSave: async () => true,
      saveable: () => true,
      setSaveError: () => {},
    });
    confirmations.requestCloseTab(buf.key);
    expect(closeBufferSpy).not.toHaveBeenCalled();
    expect(confirmations.confirm()).toEqual({
      intent: "close",
      key: buf.key,
    });

    // Unconfirmed close is blocked while memory-only recovery exists
    expect(closeFilesBuffer(PROJECT, buf.key)).toBeNull();
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);

    // Confirmed close (discardDraft: true) clears Pretty display state and recovery state for the tab
    expect(
      closeFilesBuffer(PROJECT, buf.key, { discardDraft: true }),
    ).not.toBeNull();
    expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(false);
    expect(hasUnpersistedPrettyRecoveryForDocument(docState.documentId)).toBe(
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
    await openAndAttachDocumentReplica(buf);
    render(() => (
      <FilesEditor {...editorProps(projectFilesState(PROJECT).byKey[buf.key]!)} />
    ));

    const view = getFilesEditorView(PROJECT, buf.key)!;
    fireEvent.click(screen.getByTestId("files-editor-pretty-toggle"));
    await waitFor(() => {
      expect(isPrettyDisplayEnabled(PROJECT, buf.key)).toBe(true);
    });

    const adapter = getFilesEditorPrettyAdapter(PROJECT, buf.key)!;
    const recovery = getPrettyDocumentRecoveryState(
      adapter.replica.accepted.id,
      adapter.replica.incarnation,
    );
    appendMultiCursorConflictToRecovery(
      recovery,
      "runtime-1",
      recovery.incarnation,
      1,
      1,
      "fp-1",
      {
        reason: "overlapping-raw-ranges",
        sourceRevision: 1,
        intentions: [
          {
            order: 0,
            dispFrom: 0,
            dispTo: 5,
            rawFrom: 1,
            rawTo: 6,
            insert: '"a":100,\n"extra":"very_long_multiline_value_for_button"',
          },
        ],
      },
      [{ srcFrom: 0, srcTo: rawJsonl.length }],
    );
    adapter.setRecoveryFeedback("Ready");

    const tooltip = await waitFor(() => {
      const el = document.querySelector(
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
    copyTextToClipboard.mockRejectedValueOnce(new Error("Denied"));
    const copyBtn = tooltip.querySelector(
      '[data-testid="pretty-recovery-copy"]',
    ) as HTMLButtonElement;
    fireEvent.click(copyBtn);
    await waitFor(() => {
      const feedback = document.querySelector(
        '[data-testid="pretty-recovery-feedback"]',
      );
      expect(feedback?.textContent).toContain(
        "Selected retained edit in text box",
      );
    });

    // Async clipboard success path reports copied to clipboard
    copyTextToClipboard.mockResolvedValueOnce(undefined);
    const copyBtn2 = document.querySelector(
      '[data-testid="pretty-recovery-copy"]',
    ) as HTMLButtonElement;
    fireEvent.click(copyBtn2);
    await waitFor(() => {
      const feedback = document.querySelector(
        '[data-testid="pretty-recovery-feedback"]',
      );
      expect(feedback?.textContent).toContain(
        "Copied retained edit to clipboard",
      );
    });

    // Escape inside recovery tooltip returns focus to view.contentDOM
    const focusSpy = vi.spyOn(view.contentDOM, "focus");
    const activeTooltip = document.querySelector(
      '[data-testid="pretty-recovery-tooltip"]',
    ) as HTMLElement;
    fireEvent.keyDown(activeTooltip, { key: "Escape" });
    expect(focusSpy).toHaveBeenCalled();
  });
});
