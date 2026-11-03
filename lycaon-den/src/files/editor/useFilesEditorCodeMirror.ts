import type { EditorView } from "@codemirror/view";
import { createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import {
  setFindingDiagnostics,
  type FindingDiagnosticHandlers,
} from "../../components/source/annotations/findings-diagnostics.ts";
import {
  type AttributionMark,
  type FindingLineMark,
} from "../../components/source/annotations/knowing-gutter-model.ts";
import {
  setLineGutterAttribution,
  setLineGutterFindings,
} from "../../components/source/annotations/line-gutter.ts";
import { setOverviewSymbolMarks } from "../../components/source/annotations/overview-ruler.ts";
import {
  applyEditorDisplayPrefs,
  sourceUsesSimplifiedDisplay,
} from "../../components/source/editor/codemirror-theme.ts";
import {
  observeEditorEditingStatus,
  setEditorPromptContainer,
} from "../../components/source/editor/editor-editing-style.ts";
import type { SourceReaderHandle } from "../../components/source/reader/SourceReader.tsx";
import { setSecretScreen } from "../../components/source/secrets/secret-span-decorations.ts";
import { observeSurfaceFailure } from "../../notices/surface-failure.ts";
import { registerCommandHandler } from "../../shortcuts/dispatcher.ts";
import { registerFocusRegion, releaseFocusRegion } from "../../shortcuts/focus-region.ts";
import { createResidentFocus, useResidentLive } from "../../ui/resident-activity.ts";
import {
  useResidentInteractive,
  useResidentPresence,
} from "../../ui/resident-presence-context.tsx";
import type { FilesEditorScope } from "../components/files-scope.ts";
import { editorReplica } from "../documents/editor-document.ts";
import { editorStateAction, resolveEditorIdentity } from "../documents/editor-identity.ts";
import { versionRestoreOffered } from "../history/file-version-restore.ts";
import type { FileVersionView } from "../history/file-version.ts";
import { navigateEditorChange } from "./editor-change-navigation.ts";
import {
  observeEditorDocumentMarks,
  observeEditorDocumentPresentation,
} from "./editor-document-presentation.ts";
import { observeEditorComparison } from "./editor-review-marks.ts";
import { currentDisplayPrefs } from "./files-editor-display.ts";
import { filesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import { createFilesEditorViewport } from "./files-editor-viewport.ts";

export function useFilesEditorCodeMirror(options: {
  projectId: () => string;
  sessionId: () => string | undefined;
  client: FilesEditorScope["client"];
  buffer: FilesEditorScope["buffer"];
  editable: () => boolean;
  version: () => FileVersionView | null;
  historicalActive: () => boolean;
  onCursor: (line: number, col: number) => void;
  onCursorTeleport: (line: number) => void;
  onGoToDefinitionAt: (pos: number) => void;
  onRejectHunk: () => Parameters<typeof observeEditorDocumentMarks>[0]["rejectHunk"] extends () => infer R ? R : never;
  saveError: () => string | null;
  conflict: () => boolean;
  stateActionBusy: () => boolean | undefined;
  versionRestoreDisabledReason: () => string | null;
  onRestoreVersion: (version: FileVersionView) => void;
  onOpenCurrent: () => (() => void) | undefined;
  onMakeEditable: () => (() => void) | undefined;
  onRetryEditing: () => (() => void) | undefined;
  onRestoreDeleted: () => ((versionId: string) => void) | undefined;
  onRevertFile: () => (() => void) | undefined;
}) {
  const live = useResidentLive();
  const interactive = useResidentInteractive();
  const presence = useResidentPresence();
  let view: EditorView | undefined;
  let hostEl: HTMLDivElement | undefined;
  const [editingPrompt, setEditingPrompt] = createSignal<HTMLElement>();
  const [editingStatus, setEditingStatus] = createSignal("");
  const [editorView, setEditorView] = createSignal<EditorView | undefined>();

  createEffect(() => {
    const current = editorView();
    setEditingStatus("");
    if (current) onCleanup(observeEditorEditingStatus(current, setEditingStatus));
  });
  createEffect(() => {
    const current = editorView(),
      container = editingPrompt();
    if (current && container) setEditorPromptContainer(current, container);
  });

  const [chromeReady, setChromeReady] = createSignal(false);
  /** Costly files open with wrap, guides, and highlighting off. */
  const [simplifiedDisplay, setSimplifiedDisplay] = createSignal(false);

  const editor: FilesEditorScope = {
    projectId: options.projectId,
    sessionId: options.sessionId,
    client: options.client,
    buffer: options.buffer,
    live,
    chromeReady,
    historicalActive: options.historicalActive,
    currentView: () => view,
    editorView,
  };

  const bindView = (next: EditorView | undefined) => {
    view = next;
    setEditorView(next);
  };

  const reportCursor = (line: number, col: number) => {
    if (interactive()) options.onCursor(line, col);
  };

  const filesFocusClaim = {};
  createEffect(() => {
    if (!interactive()) return;
    const target = editorView()?.contentDOM ?? hostEl;
    if (!target) return;
    const transfer = target !== hostEl && document.activeElement === hostEl;
    registerFocusRegion("files", target, filesFocusClaim);
    if (transfer) target.focus({ preventScroll: true });
    onCleanup(() => releaseFocusRegion("files", filesFocusClaim));
  });

  const bindEditorFeatures = (features: {
    currentAbsent: () => boolean;
    comparisonPreview: () => boolean;
    previewActive: () => boolean;
    deletionRestore: () =>
      | { kind: "version"; versionId: string }
      | { kind: "commit" }
      | null;
    versionHandle: () => SourceReaderHandle | undefined;
    activeReaderHandle: () => SourceReaderHandle | undefined;
    editorScopeRevision: () => string;
    deletedLines: Parameters<typeof observeEditorDocumentMarks>[0]["deletedLines"];
    dropCondenseRequest: () => void;
    condenseIfRequested: (v: EditorView) => void;
    toggleSymbolMenu: (trigger: HTMLElement) => void;
    installGutterInteractions: (v: EditorView) => void;
    attrMarks: () => AttributionMark[];
    findingMarks: () => FindingLineMark[];
    symbols: () => Parameters<typeof setOverviewSymbolMarks>[1];
    findingRecords: () => Parameters<typeof setFindingDiagnostics>[2];
    findingDiagnosticHandlers: FindingDiagnosticHandlers;
  }) => {
    const activeEditorView = () =>
      options.historicalActive()
        ? undefined
        : features.currentAbsent()
          ? undefined
          : editorView();

    createEffect(() => {
      if (!interactive()) return;
      const current = activeEditorView();
      if (!current) return;
      const head = current.state.selection.main.head;
      const line = current.state.doc.lineAt(head);
      reportCursor(line.number, head - line.from + 1);
    });

    createResidentFocus(() => {
      const target = activeEditorView()?.contentDOM;
      const active = document.activeElement;
      // A historical version has no editor target, so the host locates the view.
      const filesView = (target ?? hostEl)?.closest<HTMLElement>(".project-files-view");
      if (active instanceof HTMLElement && filesView?.contains(active)) {
        return undefined;
      }
      if (options.historicalActive()) features.versionHandle()?.focus();
      return target;
    });

    createEffect(() => {
      if (!interactive()) return;
      onCleanup(
        registerCommandHandler("editor.goToSymbol", () => {
          const trigger = hostEl
            ?.closest(".den-files-editor")
            ?.querySelector<HTMLElement>(".den-files-editor__crumb-file");
          if (trigger) features.toggleSymbolMenu(trigger);
        }),
      );
      for (const [command, direction] of [
        ["editor.nextChange", 1],
        ["editor.previousChange", -1],
      ] as const) {
        onCleanup(
          registerCommandHandler(command, () => {
            const active = activeEditorView();
            if (active) navigateEditorChange(active, direction);
          }),
        );
      }
    });

    const simplifiedStatusVisible = () => {
      if (!options.historicalActive() && !features.currentAbsent()) return simplifiedDisplay();
      const historicalView = activeEditorView();
      return historicalView ? sourceUsesSimplifiedDisplay(historicalView.state) : false;
    };

    const editorIdentity = createMemo(() =>
      resolveEditorIdentity({
        historical: versionRestoreOffered(options.buffer(), options.version()),
        workerDraft: Boolean(options.buffer().jobId) && !features.comparisonPreview(),
        deleted: features.currentAbsent(),
        absentDraft: options.buffer().absent === true,
        writable: options.buffer().writable,
        preview: features.comparisonPreview() || features.previewActive(),
        editable: options.editable(),
        loading: options.buffer().loading,
        opening: options.buffer().editorOpening,
        paused:
          options.buffer().editorResolving || !!options.buffer().editorEditingBlock,
        overLimit: options.buffer().overLimit,
        unsupported:
          options.buffer().kind !== "text" || options.buffer().encoding === null,
        documentPending:
          !editorReplica(options.buffer().documentId) && !options.buffer().loadError,
      }),
    );

    const stateAction = createMemo(() => {
      const action = editorStateAction(editorIdentity());
      if (
        action?.id === "retry-editing" &&
        (!options.onRetryEditing() || options.buffer().loadError)
      ) {
        return null;
      }
      if (action?.id === "restore-deleted" && !features.deletionRestore()) return null;
      return action;
    });

    const openingError = () => {
      if (editorIdentity().id !== "editing-error" || options.buffer().loadError) return null;
      const opening = options.buffer().editorOpening;
      return opening?.status === "error"
        ? opening.message
        : "The file could not be opened for editing.";
    };

    const activeFailure = (failure: () => string | null | undefined) => () =>
      presence() === "active" ? failure() : null;

    observeSurfaceFailure(
      {
        code: "files_save_failed",
        title: "Could not save the file",
        suggestedAction:
          "Keep editing; the text stays in the editor. Save again once the problem clears.",
      },
      activeFailure(() =>
        !options.historicalActive() && !options.conflict() ? options.saveError() : null,
      ),
      options.projectId,
    );
    observeSurfaceFailure(
      {
        code: "files_document_unavailable",
        title: "Could not load the file",
        suggestedAction: "Reload the file, or reopen it from the file tree.",
      },
      activeFailure(() =>
        !options.historicalActive() && !features.currentAbsent()
          ? options.buffer().loadError
          : null,
      ),
      options.projectId,
    );
    observeSurfaceFailure(
      {
        code: "files_editing_unavailable",
        title: "Could not open the file for editing",
        suggestedAction:
          "Use Retry editing in the editor header; the file text is unchanged.",
      },
      activeFailure(openingError),
      options.projectId,
    );

    const stateActionDisabledReason = () => {
      const action = stateAction();
      if (!action) return null;
      if (options.stateActionBusy()) return "Action in progress";
      if (action.id === "restore-version") return options.versionRestoreDisabledReason();
      if (
        action.id === "make-editable" &&
        (!options.onMakeEditable() || !options.buffer().baseSha256)
      ) {
        return "The file must be reloaded before its permission can change";
      }
      return null;
    };

    const runStateAction = () => {
      const action = stateAction();
      if (!action || stateActionDisabledReason()) return;
      switch (action.id) {
        case "restore-version": {
          const ver = options.version();
          if (ver) options.onRestoreVersion(ver);
          break;
        }
        case "open-current":
          options.onOpenCurrent()?.();
          break;
        case "make-editable":
          options.onMakeEditable()?.();
          break;
        case "retry-editing":
          options.onRetryEditing()?.();
          break;
        case "restore-deleted": {
          const restore = features.deletionRestore();
          if (restore?.kind === "version") options.onRestoreDeleted()?.(restore.versionId);
          else if (restore?.kind === "commit") options.onRevertFile()?.();
          break;
        }
      }
    };

    const installChrome = (
      v: EditorView,
      path: string,
      prefs: ReturnType<typeof currentDisplayPrefs>,
    ) => {
      features.installGutterInteractions(v);
      setLineGutterAttribution(
        v,
        untrack(() => features.attrMarks()),
      );
      setLineGutterFindings(
        v,
        untrack(() => features.findingMarks()),
      );
      setOverviewSymbolMarks(
        v,
        untrack(() => features.symbols()),
      );
      setSecretScreen(
        v,
        untrack(() => filesEditorSecretScreen(options.buffer().key)) ?? null,
      );
      setFindingDiagnostics(
        v,
        path,
        untrack(() => features.findingRecords()),
        features.findingDiagnosticHandlers,
      );
      applyEditorDisplayPrefs(v, prefs);
    };

    createFilesEditorViewport({
      ...editor,
      editable: options.editable,
      hostElement: () => hostEl,
      bindView,
      currentAbsent: features.currentAbsent,
      reportCursor,
      onCursorTeleport: options.onCursorTeleport,
      onGoToDefinitionAt: options.onGoToDefinitionAt,
      setSimplifiedDisplay,
      setChromeReady,
      installChrome,
    });

    observeEditorDocumentPresentation({
      ...editor,
      activeReaderHandle: features.activeReaderHandle,
      activeEditorView,
    });

    observeEditorComparison({
      ...editor,
      editorScopeRevision: features.editorScopeRevision,
      dropCondenseRequest: features.dropCondenseRequest,
      condenseIfRequested: features.condenseIfRequested,
    });

    observeEditorDocumentMarks({
      ...editor,
      rejectHunk: options.onRejectHunk,
      deletedLines: features.deletedLines,
    });

    return {
      activeEditorView,
      simplifiedStatusVisible,
      editorIdentity,
      stateAction,
      stateActionDisabledReason,
      runStateAction,
    };
  };

  return {
    editor,
    editorView,
    currentView: () => view,
    hostElement: () => hostEl,
    setHostElement: (el: HTMLDivElement) => {
      hostEl = el;
    },
    setEditingPrompt,
    editingStatus,
    replica: () => editorReplica(options.buffer().documentId),
    bindEditorFeatures,
  };
}
