/** Route editor actions to prose or hunk review after dirty checks. */

import { EditorView } from "@codemirror/view";
import type { LycaonClient } from "../../../api/client.ts";
import { addToChat } from "../../../chat/composer/add-to-chat.ts";
import { pathFileChipLabel } from "../../../chat/composer/path-file-ref.ts";
import { fileDisplayName } from "../../../files/components/project-files-model.ts";
import { editorSelectionRange } from "../../../files/editor/files-selection-to-chat.ts";
import { editorDocumentRevisionFor } from "../../../files/documents/editor-document.ts";
import { fetchCachedSourceSymbols } from "../../../files/source/source-symbols-cache.ts";
import {
  clearScopePreview,
  emphasizeScopePreview,
} from "../editor/codemirror-theme.ts";
import { invokeCommand } from "../../../shortcuts/dispatcher.ts";
import { armReplace } from "../../../search/replace-arm.ts";
import { wordAtPos } from "../../../files/editor/editor-symbol-target.ts";
import {
  cancelInlineEdit,
  closeInlineEdit,
  inlineEditOpen,
  inlineEditRenameScope,
  openInlineEdit,
  pushInlineEditHistory,
  setInlineEditError,
  setInlineEditRenameCounts,
  setInlineEditRunning,
  type InlineEditMode,
} from "./inline-edit-controller.ts";
import { countWholeWordOccurrences } from "./rename-card-model.ts";
import {
  editorActionOutput,
  nextTranscriptPosition,
  runEditorAction,
  type EditorActionOutput,
  type EditorActionRunResult,
  type TranscriptReadPosition,
} from "./editor-action-client.ts";
import {
  resolveInlineEditScope,
  type ResolvedInlineEditScope,
} from "./inline-edit-scope.ts";
import type { HunkReviewTarget } from "./HunkReviewPanel.tsx";
import type { SelectionVerbDef } from "../verbs/selection-verbs.ts";
import { assistantProseAfterOrd, nextUserActionOrd } from "./assistant-prose.ts";

export type EditorActionFinding = {
  id: string;
  rule: string;
  message: string;
};

export type EditorActionContext = {
  client: LycaonClient;
  projectId: string;
  sessionId: string;
  rootId: string;
  path: string;
  bufferKey: string;
  rootLabel: string;
  multiRoot: boolean;
  view: EditorView;
  hostEl: HTMLElement | null;
  /** Disk hash for symbol lookup. */
  contentSha256: string | null;
  dirty: boolean;
  ensureCleanDisk: () => Promise<"proceed" | "cancel">;
  abortSignal?: AbortSignal;
  onHunkReview: (target: HunkReviewTarget) => void;
  onError: (message: string | null) => void;
  onFocusComposer?: () => void;
};

function anchorForScope(
  view: EditorView,
  hostEl: HTMLElement | null,
  scope: ResolvedInlineEditScope,
): { top: number; left: number; width: number } {
  const line = view.state.doc.line(scope.startLine);
  const coords = view.coordsAtPos(line.from);
  const hostRect = hostEl?.getBoundingClientRect();
  if (!coords || !hostRect) {
    return { top: 48, left: 16, width: 360 };
  }
  return {
    top: coords.top - hostRect.top,
    left: coords.left - hostRect.left,
    width: Math.max(280, hostRect.width - 32),
  };
}

export async function resolveScopeForView(
  args: EditorActionContext,
): Promise<ResolvedInlineEditScope> {
  const sel = editorSelectionRange(args.view);
  const symbols = await fetchCachedSourceSymbols({
    client: args.client,
    projectId: args.projectId,
    sessionId: args.sessionId,
    rootId: args.rootId,
    path: args.path,
    sha256: args.contentSha256,
    signal: args.abortSignal,
  }).catch(() => []);
  return resolveInlineEditScope({
    selection: sel,
    caretLine: sel.startLine,
    docLines: args.view.state.doc.lines,
    symbols: symbols.map((s) => ({ name: s.name, line: s.line })),
  });
}

export function highlightScope(
  view: EditorView,
  scope: ResolvedInlineEditScope,
) {
  emphasizeScopePreview(view, scope.startLine, scope.endLine);
  if (view.state.doc.lines === 0) return;
  const line = Math.min(Math.max(scope.startLine, 1), view.state.doc.lines);
  view.dispatch({
    effects: EditorView.scrollIntoView(view.state.doc.line(line).from, {
      y: "center",
    }),
  });
}

/** Open the inline-edit or rename panel. */
export async function beginInlineEditPanel(
  ctx: EditorActionContext,
  mode: InlineEditMode = { kind: "inline" },
): Promise<void> {
  if (ctx.dirty) {
    const gate = await ctx.ensureCleanDisk();
    if (gate !== "proceed") return;
  }
  const scope = await resolveScopeForView(ctx);
  highlightScope(ctx.view, scope);
  const opened = openInlineEdit({
    projectId: ctx.projectId,
    rootId: ctx.rootId,
    path: ctx.path,
    bufferKey: ctx.bufferKey,
    scope,
    mode,
    anchor: anchorForScope(ctx.view, ctx.hostEl, scope),
  });
  if (opened && mode.kind === "rename" && mode.symbolName) {
    startRenameCounts(ctx, mode.symbolName);
  }
}

/** Count in-file and project-wide rename matches. */
function startRenameCounts(ctx: EditorActionContext, symbol: string) {
  setInlineEditRenameCounts({
    inFile: countWholeWordOccurrences(ctx.view.state.doc.toString(), symbol),
    project: null,
    pending: true,
  });
  void ctx.client
    .previewSearchReplacement({
      query: symbol,
      replacement: symbol,
      origin_project_id: ctx.projectId,
      case_sensitive: true,
      whole_word: true,
    })
    .then((preview) => {
      const open = inlineEditOpen();
      if (!open || open.mode.kind !== "rename" || open.mode.symbolName !== symbol) {
        return;
      }
      const files = preview.files ?? [];
      const matches = files.reduce((n, f) => n + (f.hunks?.length ?? 0), 0);
      setInlineEditRenameCounts({
        project: {
          matches,
          files: files.length,
          truncated: !!preview.truncated,
        },
        pending: false,
      });
    })
    .catch(() => {
      setInlineEditRenameCounts({ project: null, pending: false });
    });
}

/** Open Rename for an explicit symbol target or the word under the caret. */
export async function beginRenameCard(
  ctx: EditorActionContext,
  target?: { symbol: string; position?: number },
): Promise<void> {
  if (target?.position != null) {
    ctx.view.dispatch({ selection: { anchor: target.position } });
  }
  const word =
    target ?? wordAtPos(ctx.view.state, ctx.view.state.selection.main.head);
  await beginInlineEditPanel(ctx, {
    kind: "rename",
    symbolName: word?.symbol ?? null,
  });
}

export async function submitInlineEditPanel(
  ctx: EditorActionContext,
  instruction: string,
): Promise<void> {
  const open = inlineEditOpen();
  if (!open) return;
  if (open.mode.kind === "rename") {
    const symbol = open.mode.symbolName;
    if (!symbol) return;
    if (inlineEditRenameScope() === "everywhere") {
      armReplace({
        originProjectId: ctx.projectId,
        query: symbol,
        replacement: instruction.trim(),
        wholeWord: true,
        caseSensitive: true,
        regex: false,
        renameFrom: symbol,
      });
      closeInlineEdit();
      invokeCommand("search.replaceInProject");
      return;
    }
    setInlineEditRunning(true, "Renaming symbol…");
    try {
      await executeEditorAction(ctx, {
        commandId: EDITOR_ACTION_COMMANDS.renameSymbol,
        scope: open.scope,
        instruction,
        symbol,
      });
      setInlineEditRunning(false);
      closeInlineEdit();
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        cancelInlineEdit();
        return;
      }
      setInlineEditError(err instanceof Error ? err.message : "Rename failed");
    }
    return;
  }
  pushInlineEditHistory(instruction);
  setInlineEditRunning(true, "Rewriting selection…");
  try {
    await executeEditorAction(ctx, {
      commandId: EDITOR_ACTION_COMMANDS.inlineEdit,
      scope: open.scope,
      instruction,
    });
    setInlineEditRunning(false);
    closeInlineEdit();
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") {
      cancelInlineEdit();
      return;
    }
    setInlineEditError(
      err instanceof Error ? err.message : "Inline edit failed",
    );
  }
}

export async function runSelectionVerbAction(
  ctx: EditorActionContext,
  args: {
    verb: SelectionVerbDef;
    finding?: EditorActionFinding | null;
  },
): Promise<void> {
  let started = false;
  try {
    if (ctx.dirty && await ctx.ensureCleanDisk() !== "proceed") return;
    const scope = await resolveScopeForView(ctx);
    if (ctx.abortSignal?.aborted) return;
    highlightScope(ctx.view, scope);
    started = true;
    setInlineEditRunning(true, args.verb.progressLabel);
    await executeEditorAction(ctx, {
      commandId: args.verb.commandId,
      scope,
      symbol: scope.symbolName,
      finding: args.finding,
    });
  } catch (error) {
    if (ctx.abortSignal?.aborted || error instanceof DOMException && error.name === "AbortError") return;
    ctx.onError(error instanceof Error ? error.message : "The editor action failed.");
  } finally {
    if (started) {
      setInlineEditRunning(false);
      clearScopePreview(ctx.view);
    }
  }
}

/** Commands run after panel input. */
const EDITOR_ACTION_COMMANDS = {
  inlineEdit: "painted-wolf/platform:editor-run-inline-edit",
  renameSymbol: "painted-wolf/platform:editor-run-rename-symbol",
} as const;

async function executeEditorAction(
  ctx: EditorActionContext,
  args: {
    commandId: string;
    scope: ResolvedInlineEditScope;
    instruction?: string;
    symbol?: string;
    finding?: EditorActionFinding | null;
  },
): Promise<void> {
  ctx.onError(null);
  const beforeRead = await ctx.client.getProjectSource(ctx.projectId, ctx.path, {
    rootId: ctx.rootId,
    sessionId: ctx.sessionId,
  });
  const before = beforeRead.content ?? "";

  // The host rejects writes against a different document revision.
  const revision = editorDocumentRevisionFor(ctx.projectId, ctx.rootId, ctx.path);
  const output: EditorActionOutput = editorActionOutput(args.commandId);
  const action = await runEditorAction({
    client: ctx.client,
    projectId: ctx.projectId,
    sessionId: ctx.sessionId,
    commandId: args.commandId,
    signal: ctx.abortSignal,
    context: {
      root_id: ctx.rootId,
      path: ctx.path,
      start_line: args.scope.startLine,
      end_line: args.scope.endLine,
      instruction: args.instruction,
      symbol: args.symbol,
      finding_id: args.finding?.id,
      ...(revision === null ? {} : { document_revision: revision }),
    },
  });

  if (output === "prose") {
    const prose = await readAssistantProseAfter(ctx.client, ctx.sessionId, action);
    if (!prose) {
      throw new Error("The editor action completed without an assistant response.");
    }
    const name = pathFileChipLabel({
      path: ctx.path,
      name: fileDisplayName(ctx.path),
      rootLabel: ctx.rootLabel,
      multiRoot: ctx.multiRoot,
    });
    await addToChat(
      {
        kind: "path-file",
        projectId: ctx.projectId,
        rootId: ctx.rootId,
        path: ctx.path,
        name,
        startLine: args.scope.startLine,
        endLine: args.scope.endLine,
      },
      {
        destination: {
          projectId: ctx.projectId,
          sessionId: ctx.sessionId,
        },
      },
    );
    ctx.onFocusComposer?.();
    return;
  }

  const afterRead = await ctx.client.getProjectSource(ctx.projectId, ctx.path, {
    rootId: ctx.rootId,
    sessionId: ctx.sessionId,
  });
  const after = afterRead.content ?? "";
  if (after === before) {
    throw new Error("No changes were made to this file. Review the conversation for the action’s result.");
  }
  if (!afterRead.sha256 || !afterRead.encoding) {
    throw new Error("Applied file did not return editable source identity");
  }
  ctx.onHunkReview({
    projectId: ctx.projectId,
    sessionId: ctx.sessionId,
    rootId: ctx.rootId,
    path: ctx.path,
    before,
    after,
    headSha256: afterRead.sha256,
    encoding: afterRead.encoding,
  });
}

async function readAssistantProseAfter(
  client: LycaonClient,
  sessionId: string,
  action: EditorActionRunResult,
): Promise<string | null> {
  let position: TranscriptReadPosition = { afterMessageId: action.actionMessageId };
  let prose: string | null = null;
  for (;;) {
    const page = await client.listSessionMessages(sessionId, { ...position, limit: 500 });
    prose = assistantProseAfterOrd(page.messages, action.actionOrd) ?? prose;
    if (nextUserActionOrd(page.messages, action.actionOrd) !== undefined || !page.after_cursor) return prose;
    if (page.messages.length === 0) {
      throw new Error("The editor action transcript did not advance.");
    }
    position = nextTranscriptPosition(page, position);
  }
}
