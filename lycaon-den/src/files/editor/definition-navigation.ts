import { closeCompletion } from "@codemirror/autocomplete";
import type { EditorView } from "@codemirror/view";
import { createEffect, createSignal, on, onCleanup } from "solid-js";
import type { SourceDefinitionCandidate } from "../../api/types.ts";
import { clearSymbolPending, markSymbolPending } from "../../components/source/editor/codemirror-theme.ts";
import { isComposedBufferKind } from "../documents/project-files-buffer-kind.ts";
import { openFilesBuffer } from "../documents/project-files-buffers.ts";
import type { FilesScope } from "../components/files-scope.ts";
import { pushProjectJump } from "../components/project-files-jump-bridge.ts";
import { DEFINITION_PENDING_NOTICE_DELAY_MS, definitionJumpOutcome, definitionMissCopy, definitionPendingCopy } from "./definition-jump.ts";
import { wordAtPos } from "./editor-symbol-target.ts";
import { getFilesEditorView } from "./files-editor-host.ts";

type DefinitionNavigationOptions = Pick<FilesScope, "projectId" | "state" | "activeBuffer" | "client" |
  "sourceSessionId" | "rootLabelFor"> & {
  cursor: () => { line: number; col: number };
};

export function createDefinitionNavigation({
  projectId, state, activeBuffer, client, sourceSessionId, rootLabelFor, cursor,
}: DefinitionNavigationOptions) {
  const [definitionNotice, setDefinitionNotice] = createSignal<string | null>(null);
  const [definitionPicker, setDefinitionPicker] = createSignal<{
    symbol: string;
    candidates: readonly SourceDefinitionCandidate[];
    truncated: boolean;
    selected: number;
  } | null>(null);
  let definitionRequestSeq = 0;
  let definitionPendingView: EditorView | null = null;
  let definitionPendingTimer: ReturnType<typeof setTimeout> | undefined;
  let definitionNoticeTimer: ReturnType<typeof setTimeout> | undefined;
  const flashDefinitionNotice = (message: string) => {
    setDefinitionNotice(message);
    if (definitionNoticeTimer) clearTimeout(definitionNoticeTimer);
    definitionNoticeTimer = setTimeout(() => setDefinitionNotice(null), 3000);
  };

  const jumpToDefinitionCandidate = (c: SourceDefinitionCandidate) => {
    const buf = activeBuffer();
    if (buf) {
      pushProjectJump(projectId(), {
        bufferKey: buf.key,
        rootId: buf.rootId,
        path: buf.path,
        ...(buf.jobId ? { jobId: buf.jobId } : {}),
        line: cursor().line,
      });
    }
    setDefinitionPicker(null);
    const key = openFilesBuffer(projectId(), {
      rootId: c.root_id,
      rootLabel: rootLabelFor(c.root_id),
      path: c.path,
      intent: "transient",
      revealLine: c.line,
    });
    // Record the destination for forward navigation.
    pushProjectJump(projectId(), {
      bufferKey: key,
      rootId: c.root_id,
      path: c.path,
      line: c.line,
    });
  };

  const cancelGoToDefinition = () => {
    if (!definitionPendingView) return;
    definitionRequestSeq++;
    if (definitionPendingTimer) clearTimeout(definitionPendingTimer);
    definitionPendingTimer = undefined;
    clearSymbolPending(definitionPendingView);
    definitionPendingView = null;
    setDefinitionNotice(null);
  };

  const runGoToDefinition = async (pos?: number) => {
    const buf = activeBuffer();
    const c = client();
    if (!buf || isComposedBufferKind(buf.kind) || !c) return;
    const view = getFilesEditorView(projectId(), buf.key);
    if (!view) return;
    const at = pos ?? view.state.selection.main.head;
    const grabbed = wordAtPos(view.state, at);
    if (!grabbed) return;
    const seq = ++definitionRequestSeq;
    setDefinitionPicker(null);
    setDefinitionNotice(null);
    markSymbolPending(view, grabbed.from, grabbed.to);
    definitionPendingView = view;
    if (definitionPendingTimer) clearTimeout(definitionPendingTimer);
    definitionPendingTimer = setTimeout(() => {
      if (seq !== definitionRequestSeq) return;
      if (definitionNoticeTimer) clearTimeout(definitionNoticeTimer);
      setDefinitionNotice(definitionPendingCopy(grabbed.symbol));
    }, DEFINITION_PENDING_NOTICE_DELAY_MS);
    const settlePending = () => {
      if (definitionPendingTimer) clearTimeout(definitionPendingTimer);
      definitionPendingTimer = undefined;
      clearSymbolPending(view);
      definitionPendingView = null;
      setDefinitionNotice(null);
    };
    let response: {
      candidates: SourceDefinitionCandidate[];
      truncated: boolean;
    };
    try {
      response = await c.resolveProjectSourceDefinition(
        projectId(),
        {
          root_id: buf.rootId,
          path: buf.path,
          symbol: grabbed.symbol,
          line: grabbed.line,
        },
        sourceSessionId(),
      );
    } catch {
      // A superseding request controls the pending mark.
      if (seq !== definitionRequestSeq) return;
      settlePending();
      flashDefinitionNotice(
        `Couldn’t look up definition for ${grabbed.symbol}`,
      );
      return;
    }
    if (seq !== definitionRequestSeq) return;
    settlePending();
    const outcome = definitionJumpOutcome(grabbed.symbol, response);
    if (outcome.kind === "noop") return;
    if (outcome.kind === "jump") {
      jumpToDefinitionCandidate(outcome.candidate);
      return;
    }
    if (outcome.kind === "miss") {
      flashDefinitionNotice(
        definitionMissCopy(outcome.symbol, outcome.truncated),
      );
      return;
    }
    closeCompletion(view);
    setDefinitionPicker({
      symbol: outcome.symbol,
      candidates: outcome.candidates,
      truncated: outcome.truncated,
      selected: 0,
    });
  };

  const observeKeyboardAndBuffer = () => {
    // Capture Escape for the active definition lookup.
    const onDefinitionEscape = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || !definitionPendingView) return;
      e.preventDefault();
      e.stopPropagation();
      cancelGoToDefinition();
    };
    window.addEventListener("keydown", onDefinitionEscape, true);
    onCleanup(() => {
      window.removeEventListener("keydown", onDefinitionEscape, true);
      if (definitionPendingTimer) clearTimeout(definitionPendingTimer);
    });
  
    // Changing buffers cancels the active lookup.
    createEffect(
      on(
        () => state().activeKey,
        () => cancelGoToDefinition(),
        { defer: true },
      ),
    );
  };

  return {
    definitionNotice, definitionPicker, setDefinitionPicker,
    jumpToDefinitionCandidate, runGoToDefinition,
    clearNoticeTimer: () => { if (definitionNoticeTimer) clearTimeout(definitionNoticeTimer); },
    observeKeyboardAndBuffer,
  };
}
