import type { IgnoreSecretTarget } from "../../components/source/secrets/IgnoreSecretDialog.tsx";
import { navigateFromStatusChip, statusChipNavigationAvailable } from "../../chat/status/status-navigation-sink.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import { emphasizeScopePreview } from "../../components/source/editor/codemirror-theme.ts";
import { beginRenameCard, resolveScopeForView } from "../../components/source/inline-edit/run-editor-action.ts";
import { secretSpanFact } from "../../components/source/secrets/secret-span-copy.ts";
import { getSecretSpanMarks } from "../../components/source/secrets/secret-span-decorations.ts";
import { siblingSecretSpans } from "../../components/source/secrets/secret-span-model.ts";
import { type EditorSymbolTarget } from "./editor-symbol-target.ts";
import { selectSiblingSpans } from "./files-secret-actions.ts";
import { type FilesContextMenuActions } from "../commands/project-files-context-menu.ts";
import type { HunkReviewTarget } from "../../components/source/inline-edit/HunkReviewPanel.tsx";
import type { FileBufferKey } from "../components/project-files-model.ts";
import { EditorView } from "@codemirror/view";
import { createSignal } from "solid-js";
import {
  runSelectionVerbAction,
  type EditorActionContext,
} from "../../components/source/inline-edit/run-editor-action.ts";
import { type MarkSecretTarget } from "../../components/source/secrets/MarkSecretDialog.tsx";
import { SECRET_SPAN_COPY } from "../../components/source/secrets/secret-span-copy.ts";
import { secretScreenSummary } from "../../components/source/secrets/secret-span-model.ts";
import { selectionVerbById, type SelectionVerbId } from "../../components/source/verbs/selection-verbs.ts";
import { syncEditorDocumentDraft } from "../documents/editor-document.ts";
import { flushFilesDraftSyncFor } from "../documents/files-draft-sync.ts";
import { findingOverlappingRange } from "./files-editor-findings.ts";
import { getFilesEditorView } from "./files-editor-host.ts";
import { filesEditorSecretScreen } from "./files-editor-secret-screen.ts";
import {
  anchorSecretSelection,
  secretActionRange,
  type SecretActionTarget,
} from "./files-secret-actions.ts";
import { addEditorSelectionToChat, editorSelectionRange } from "./files-selection-to-chat.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import type { FilesScope } from "../components/files-scope.ts";

type FilesEditorActionsOptions = Pick<FilesScope, "projectId" | "client" | "activeBuffer" | "state" | "filesRoots" |
  "versionForBuffer" | "setSaveError" | "copyAbsolutePathRef" | "copyRelativePathRef"> & {
  /** The selected chat, which need not belong to this project. */
  sessionId: () => string | undefined;
  editorPaneEl: () => HTMLDivElement | undefined;
  ensureCleanDiskForEditorAction: (key: FileBufferKey) => Promise<"proceed" | "cancel">;
  runGoToDefinition: (position: number) => unknown;
};

export function createFilesEditorActions(options: FilesEditorActionsOptions) {
  const { projectId, sessionId: currentSessionId, client, activeBuffer, state, filesRoots, versionForBuffer,
    editorPaneEl, ensureCleanDiskForEditorAction, setSaveError,
    copyAbsolutePathRef, copyRelativePathRef, runGoToDefinition } = options;
  const [ignoreSecretTarget, setIgnoreSecretTarget] = createSignal<IgnoreSecretTarget | null>(null);
  let editorActionAbort: AbortController | null = null;
  const handoffSelectionToChat = (capturedView?: EditorView) => {
    const buf = activeBuffer();
    if (!buf || isComposedBufferKind(buf.kind)) return;
    const version = versionForBuffer(buf);
    const view = capturedView ?? getFilesEditorView(projectId(), buf.key);
    const rootId = version?.rootId ?? buf.rootId;
    void addEditorSelectionToChat({
      projectId: projectId(),
      rootId,
      path: version?.path ?? buf.path,
      rootLabel:
        filesRoots().find((root) => root.id === rootId)?.label ?? buf.rootLabel,
      multiRoot: filesRoots().length > 1,
      view,
    });
  };

  const [markSecretTarget, setMarkSecretTarget] =
    createSignal<MarkSecretTarget | null>(null);
  const [hunkReview, setHunkReview] = createSignal<HunkReviewTarget | null>(null);

  /** Resolves project-level secret actions without a chat session. */
  const secretActionTarget = (): SecretActionTarget | null => {
    const c = client();
    const buf = activeBuffer();
    if (!c || !buf || isComposedBufferKind(buf.kind) || !buf.documentId) return null;
    const view = getFilesEditorView(projectId(), buf.key);
    if (!view) return null;
    const key = buf.key;
    return {
      projectId: projectId(),
      documentId: buf.documentId,
      view,
      sync: async () => {
        // Publish the latest debounced draft first.
        flushFilesDraftSyncFor(projectId(), key);
        const buffer = state().byKey[key];
        if (!buffer || isComposedBufferKind(buffer.kind)) return null;
        return syncEditorDocumentDraft(projectId(), buffer);
      },
    };
  };

  /** Reports screening coverage and findings. */
  const secretScreenReport = (): string => {
    const buf = activeBuffer();
    if (!buf) return SECRET_SPAN_COPY.reportTitle;
    const version = versionForBuffer(buf);
    const screen = version
      ? version.secretScreen
      : filesEditorSecretScreen(buf.key);
    const summary = secretScreenSummary(screen);
    if (summary.gap === "not_screened") return SECRET_SPAN_COPY.chipNotScreenedTip;
    const parts: string[] = [
      summary.total > 0
        ? SECRET_SPAN_COPY.reportCounts(summary)
        : SECRET_SPAN_COPY.reportNone,
    ];
    if (summary.gap === "truncated") {
      parts.push(SECRET_SPAN_COPY.chipTruncatedTip(screen?.screened_bytes ?? 0));
    }
    if (summary.screenedRevision != null) {
      parts.push(SECRET_SPAN_COPY.reportRevision(summary.screenedRevision));
    }
    parts.push(
      summary.catalogVersion
        ? SECRET_SPAN_COPY.reportCatalog(summary.catalogVersion)
        : SECRET_SPAN_COPY.reportNoCatalog,
    );
    parts.push(SECRET_SPAN_COPY.reportNeverClean);
    return parts.join(" · ");
  };

  const openMarkSecretDialog = (at?: number | null) => {
    const c = client();
    const target = secretActionTarget();
    const buf = activeBuffer();
    if (!c || !target || !buf) return;
    const range = secretActionRange(target.view, at);
    if (!range) return;
    const line = target.view.state.doc.lineAt(range.from).number;
    const selection = anchorSecretSelection(target, range.from, range.to);
    setMarkSecretTarget({
      path: buf.path,
      line,
      dirty: buf.dirty,
      preview: (trim) => selection.preview(c, trim),
      mark: async (args) => {
        const secret = await selection.mark(c, { ...args, operationId: crypto.randomUUID() });
        return secret.name;
      },
    });
  };

  const ignoreSecretSpanAction = (at?: number | null) => {
    const target = secretActionTarget();
    const buffer = activeBuffer();
    if (!target || !buffer) return;
    const span = secretActionRange(target.view, at)?.span;
    if (!span || span.state !== "detected") return;
    const value = target.view.state.doc.sliceString(span.from, span.to);
    setIgnoreSecretTarget({ projectId: projectId(), rootId: buffer.rootId, value: async () => value });
  };

  const buildEditorActionContext = (): EditorActionContext | null => {
    const c = client();
    const buf = activeBuffer();
    const sessionId = currentSessionId()?.trim();
    if (!c || !buf || isComposedBufferKind(buf.kind) || !sessionId) return null;
    const view = getFilesEditorView(projectId(), buf.key);
    if (!view) return null;
    editorActionAbort?.abort();
    editorActionAbort = new AbortController();
    return {
      client: c,
      projectId: projectId(),
      sessionId,
      rootId: buf.rootId,
      path: buf.path,
      bufferKey: buf.key,
      rootLabel: buf.rootLabel,
      multiRoot: filesRoots().length > 1,
      view,
      hostEl: editorPaneEl() ?? null,
      contentSha256: buf.baseSha256,
      dirty: buf.dirty,
      ensureCleanDisk: () => ensureCleanDiskForEditorAction(buf.key),
      abortSignal: editorActionAbort.signal,
      onHunkReview: (target) => setHunkReview(target),
      onError: setSaveError,
      onFocusComposer: () => {
        /* Shell controls composer focus. */
      },
    };
  };

  const runEditorVerb = (verbId: SelectionVerbId) => {
    const ctx = buildEditorActionContext();
    if (!ctx) return;
    const verb = selectionVerbById(verbId);
    const finding =
      verbId === "fixFinding"
        ? findingOverlappingRange(
            ctx.bufferKey,
            editorSelectionRange(ctx.view).startLine,
            editorSelectionRange(ctx.view).endLine,
          )
        : null;
    if (verbId === "fixFinding" && !finding) return;
    void runSelectionVerbAction(ctx, {
      verb,
      finding: finding
        ? {
            id: finding.fingerprints?.primary ?? finding.rule_id ?? "finding",
            rule: finding.rule_id ?? "",
            message: finding.message ?? "",
          }
        : null,
    });
  };

  const editorContextActions = (capturedSymbolTarget: EditorSymbolTarget, capturedEditorView?: EditorView,
    capturedPosition?: number | null): Pick<FilesContextMenuActions,
    "editorCopyPath" | "editorCopyRelativePath" | "editorAddSelectionToChat" | "editorSymbol" | "editorSelectionVerb" | "renameShortcutHint" | "goToDefinitionShortcutHint" | "editorEmphasizeScope" | "editorSecret" | "editorHasFinding" | "addSelectionShortcutHint"> => ({
    editorCopyPath: () => {
      const buf = activeBuffer();
      if (!buf) return;
      const version = versionForBuffer(buf);
      copyAbsolutePathRef(version?.rootId ?? buf.rootId, version?.path ?? buf.path);
    },
    editorCopyRelativePath: () => {
      const buf = activeBuffer();
      if (!buf) return;
      const version = versionForBuffer(buf);
      copyRelativePathRef(version?.rootId ?? buf.rootId, version?.path ?? buf.path);
    },
    editorAddSelectionToChat: () =>
      void handoffSelectionToChat(capturedEditorView),
    editorSymbol: {
      target: capturedSymbolTarget,
      rename: () => {
        if (capturedSymbolTarget.kind !== "symbol") return;
        const ctx = buildEditorActionContext();
        if (ctx) {
          void beginRenameCard(ctx, {
            symbol: capturedSymbolTarget.symbol,
            position: capturedSymbolTarget.from,
          });
        }
      },
      goToDefinition: () => {
        if (capturedSymbolTarget.kind !== "symbol") return;
        void runGoToDefinition(capturedSymbolTarget.from);
      },
    },
    editorSelectionVerb: (verb) => void runEditorVerb(verb),
    renameShortcutHint: bindingForHandler("editor.renameSymbol"),
    goToDefinitionShortcutHint: bindingForHandler("files.goToDefinition"),
    editorEmphasizeScope: () => {
      void (async () => {
        const ctx = buildEditorActionContext();
        if (!ctx) return;
        const scope = await resolveScopeForView(ctx);
        emphasizeScopePreview(ctx.view, scope.startLine, scope.endLine);
      })();
    },
    editorSecret: (() => {
      const buf = activeBuffer();
      if (!buf || isComposedBufferKind(buf.kind)) return undefined;
      const historical = versionForBuffer(buf) != null;
      if (!historical && !buf.documentId) return undefined;
      const view = capturedEditorView ??
        getFilesEditorView(projectId(), buf.key);
      if (!view) return undefined;
      const range = secretActionRange(view, capturedPosition);
      const span = range?.span ?? null;
      const selectionEligible = !view.state.selection.main.empty;
      // The menu acts on the range captured at the click, not on the caret it left behind.
      const markSelection = () => openMarkSecretDialog(capturedPosition);
      return {
        span: span
          ? {
              fact: secretSpanFact(span),
              state: span.state,
              reference: span.reference,
              siblings: siblingSecretSpans(getSecretSpanMarks(view), span).length,
            }
          : null,
        ...(!historical && (span || selectionEligible)
          ? { markSelection }
          : {}),
        ...(!historical && span?.state === "detected"
          ? { trackSpan: markSelection }
          : {}),
        ...(!historical && span?.state === "detected"
          ? { ignoreSpan: () => ignoreSecretSpanAction(capturedPosition) }
          : {}),
        ...(span && span.reference
          ? {
              copyReference: () => {
                void copyTextToClipboard(span.reference);
              },
            }
          : {}),
        ...(span
          ? { selectSiblings: () => selectSiblingSpans(view, span) }
          : {}),
        ...(span && statusChipNavigationAvailable()
          ? {
              openInSecrets: () =>
                navigateFromStatusChip({
                  kind: "project-configuration",
                  section: "secrets",
                }),
            }
          : {}),
      };
    })(),
    editorHasFinding: (() => {
      const buf = activeBuffer();
      if (!buf || isComposedBufferKind(buf.kind)) return false;
      const view = getFilesEditorView(projectId(), buf.key);
      if (!view) return false;
      const sel = editorSelectionRange(view);
      return (
        findingOverlappingRange(buf.key, sel.startLine, sel.endLine) != null
      );
    })(),
    addSelectionShortcutHint: bindingForHandler("files.addSelectionToChat"),
  });
  const abortEditorAction = () => editorActionAbort?.abort();
  return {
    hunkReview,
    setHunkReview,
    handoffSelectionToChat,
    ignoreSecretTarget,
    setIgnoreSecretTarget,
    markSecretTarget,
    setMarkSecretTarget,
    secretScreenReport,
    openMarkSecretDialog,
    buildEditorActionContext,
    abortEditorAction,
    editorContextActions,
  };
}
