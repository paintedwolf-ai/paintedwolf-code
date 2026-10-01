import { sessionTitle } from "../../chat/session/session-title.ts";
import { EditorView } from "@codemirror/view";
import { createEffect, createSignal, untrack } from "solid-js";
import type { SecurityFinding } from "../../api/types.ts";
import { openFinding } from "../../platform/navigation/open-finding.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { type FindingDiagnosticHandlers } from "../../components/source/annotations/findings-diagnostics.ts";
import { type AttributionContributor } from "../../components/source/annotations/knowing-gutter-model.ts";
import { agentChats } from "../components/agent-presence.ts";
import { agentPresenceFor } from "../components/agent-presence-store.ts";
import { readAgentPalette } from "../../contributions/agent-color-palette.ts";
import { type LineFactsCardState } from "../../components/source/annotations/LineFactsCard.tsx";
import { lineFacts, type LineFactRow } from "../../components/source/annotations/line-facts.ts";
import { setLineFactsHandlers } from "../../components/source/annotations/line-facts-gutter.ts";
import { rememberSourceReturn } from "../source/source-return.ts";
import {
  editorLineFactsInput,
  restoreHunkForFold,
  setLineGutterCutHandlers,
  setLineGutterFindingHandlers,
  setLineGutterProvenanceHandlers,
} from "../../components/source/annotations/line-gutter.ts";
import { type RemovedLinesPreview } from "../../components/source/diff/RemovedLinesPopover.tsx";
import {
  openScopeDeletion,
  renderScopeDeletedLines,
  setScopeDeletionPeek,
  type ScopeDeletionFold,
} from "../../components/source/diff/scope-diff.ts";
import { pushProjectJump } from "../components/project-files-jump-bridge.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import type { DiffLineHunk } from "../review/line-diff.ts";
import type { FilesEditorScope } from "../components/files-scope.ts";

type EditorLineFactsOptions = Pick<FilesEditorScope, "projectId" | "buffer" | "client" | "currentView" | "editorView"> & {
  cursorLine: () => number;
  findingsScanId: () => string;
  revealInTranscript: () => ((target: TranscriptRevealTarget) => void) | undefined;
  rejectHunk: () => ((hunk: DiffLineHunk) => void) | undefined;
};

export function createEditorLineFacts(options: EditorLineFactsOptions) {
  const { projectId, buffer, editorView, cursorLine, findingsScanId } = options;
  const binding = { get view() { return options.currentView(); } };
  const callbacks = {
    get client() { return options.client(); },
    get onRevealInTranscript() { return options.revealInTranscript(); },
    get onRejectHunk() { return options.rejectHunk(); },
  };
  const sessionTitleCache = new Map<string, string>();
  const sessionTitleRequests = new Set<string>();
  let hidePopoverTimer: ReturnType<typeof setTimeout> | undefined;
  const [lineFactsCard, setLineFactsCard] = createSignal<LineFactsCardState | null>(null);
  let showPopoverTimer: ReturnType<typeof setTimeout> | undefined;
  let returningGutterFocus = false;
  const cancelHidePopover = () => {
    clearTimeout(hidePopoverTimer);
    hidePopoverTimer = undefined;
  };
  const hideLineFacts = () => {
    clearTimeout(showPopoverTimer);
    showPopoverTimer = undefined;
    cancelHidePopover();
    const held = lineFactsCard();
    held?.anchor.classList.remove("is-open");
    held?.anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "false");
    setLineFactsCard(null);
  };
  const scheduleHidePopover = () => {
    clearTimeout(showPopoverTimer);
    showPopoverTimer = undefined;
    cancelHidePopover();
    hidePopoverTimer = setTimeout(hideLineFacts, 160);
  };
  const returnLineFactsFocus = () => {
    const button = lineFactsCard()?.anchor.querySelector<HTMLButtonElement>(".files-line-gutter__facts");
    hideLineFacts();
    returningGutterFocus = true;
    button?.focus({ preventScroll: true });
    returningGutterFocus = false;
  };
  const showLineFacts = (v: EditorView, line: number, anchor: HTMLElement, enter = false) => {
    const palette = readAgentPalette(v.dom.ownerDocument.documentElement);
    const activeChats = new Map([...agentChats(agentPresenceFor(projectId()))].map(([id, chat]) =>
      [id, { title: chat.title, color: palette?.(chat.slot).caret }] as const));
    const facts = lineFacts(line, { ...editorLineFactsInput(v.state), activeChats, sessionTitles: Object.fromEntries(sessionTitleCache) });
    if (!facts.count || !anchor.isConnected) { hideLineFacts(); return; }
    const held = lineFactsCard();
    if (held?.anchor !== anchor) {
      held?.anchor.classList.remove("is-open");
      held?.anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "false");
    }
    anchor.classList.add("is-open");
    anchor.querySelector(".files-line-gutter__facts")?.setAttribute("aria-expanded", "true");
    setLineFactsCard({ anchor, line, path: buffer().path, facts, enter });
    for (const row of facts.changed) {
      if (row.target.kind !== "chat") continue;
      const sessionId = row.target.sessionId;
      if (!sessionId || sessionTitleCache.has(sessionId) || sessionTitleRequests.has(sessionId) || !callbacks.client) continue;
      sessionTitleRequests.add(sessionId);
      void callbacks.client.getSession(sessionId).then(session => {
        sessionTitleCache.set(sessionId, sessionTitle(session.title));
        const current = lineFactsCard();
        if (current && binding.view === v) showLineFacts(v, current.line, current.anchor, current.enter);
      }).catch(() => undefined).finally(() => sessionTitleRequests.delete(sessionId));
    }
  };

  // Removal previews overlay the code without moving it.
  const [removedPreview, setRemovedPreview] = createSignal<{
    preview: RemovedLinesPreview;
    fold: ScopeDeletionFold;
  } | null>(null);
  let showRemovedTimer: ReturnType<typeof setTimeout> | undefined;
  let hideRemovedTimer: ReturnType<typeof setTimeout> | undefined;
  const clearRemovedTimers = () => {
    if (showRemovedTimer) clearTimeout(showRemovedTimer);
    if (hideRemovedTimer) clearTimeout(hideRemovedTimer);
    showRemovedTimer = hideRemovedTimer = undefined;
  };
  const hideRemovedPreview = () => {
    clearRemovedTimers();
    setRemovedPreview(null);
    const v = editorView();
    if (v) setScopeDeletionPeek(v, null);
  };
  const keepRemovedPreview = () => {
    if (hideRemovedTimer) clearTimeout(hideRemovedTimer);
    hideRemovedTimer = undefined;
  };
  const scheduleHideRemovedPreview = () => {
    if (showRemovedTimer) clearTimeout(showRemovedTimer);
    showRemovedTimer = undefined;
    keepRemovedPreview();
    hideRemovedTimer = setTimeout(hideRemovedPreview, 160);
  };
  const showRemovedPreview = (v: EditorView, fold: ScopeDeletionFold, anchor: DOMRect) => {
    hideLineFacts();
    clearRemovedTimers();
    const style = getComputedStyle(v.contentDOM);
    setRemovedPreview({
      fold,
      preview: {
        anchor,
        fromLine: fold.fromLine,
        toLine: fold.toLine,
        rows: renderScopeDeletedLines(v.state, fold.chunk),
        themeClasses: v.themeClasses,
        font: {
          "font-family": style.fontFamily,
          "font-size": style.fontSize,
          "line-height": style.lineHeight,
          "tab-size": style.tabSize,
        },
      },
    });
    setScopeDeletionPeek(v, fold.at);
  };
  const openRemovedInPlace = () => {
    const held = removedPreview();
    const v = editorView();
    hideRemovedPreview();
    if (held && v) openScopeDeletion(v, held.fold);
  };
  const rejectRemoved = () => {
    const held = removedPreview();
    const v = editorView();
    hideRemovedPreview();
    if (!held || !v || buffer().dirty) return;
    const hunk = restoreHunkForFold(v, held.fold);
    if (hunk) callbacks.onRejectHunk?.(hunk);
  };

  const rememberReturn = (line: number) => rememberSourceReturn({
    projectId: projectId(), bufferKey: buffer().key, rootId: buffer().rootId,
    path: buffer().path, jobId: buffer().jobId, line,
  });
  const jumpToAttribution = (mark: AttributionContributor, line = cursorLine()) => {
    const sessionId = mark.sessionId.trim();
    if (!sessionId || !callbacks.onRevealInTranscript) return;
    pushProjectJump(projectId(), {
      bufferKey: buffer().key,
      rootId: buffer().rootId,
      path: buffer().path,
      ...(buffer().jobId ? { jobId: buffer().jobId } : {}),
      line,
    });
    rememberReturn(line);
    hideLineFacts();
    callbacks.onRevealInTranscript({
      sessionId,
      anchor: mark.toolCallId
        ? { chicklet: "tool", anchorId: mark.toolCallId }
        : undefined,
    });
  };

  const openFindingRecord = (finding: SecurityFinding, line = cursorLine()) => {
    const fp = finding.fingerprints.primary?.trim();
    if (!fp) return;
    pushProjectJump(projectId(), {
      bufferKey: buffer().key,
      rootId: buffer().rootId,
      path: buffer().path,
      ...(buffer().jobId ? { jobId: buffer().jobId } : {}),
      line,
    });
    rememberReturn(line);
    hideLineFacts();
    openFinding({
      projectId: projectId(),
      rootId: buffer().rootId,
      fingerprint: fp,
      scanId: findingsScanId() || undefined,
    });
  };

  const activateLineFact = (row: LineFactRow, line: number) => {
    const target = row.target;
    if (target.kind === "finding") { openFindingRecord(target.finding, line); return; }
    if (!callbacks.onRevealInTranscript) return;
    rememberReturn(line);
    hideLineFacts();
    callbacks.onRevealInTranscript({ sessionId: target.sessionId,
      workerId: target.jobId,
      checkpointId: target.kind === "chat" ? target.checkpointId : undefined,
      anchor: target.kind === "chat" && target.toolCallId ? { chicklet: "tool", anchorId: target.toolCallId } : undefined });
  };

  /** Finding fixes use the selected range through the editor action. */
  const findingDiagnosticHandlers: FindingDiagnosticHandlers = {
    onOpen: openFindingRecord,
    onFix: () => {
      invokeCommand("editor.verb.fixFinding");
    },
  };

  const installGutterInteractions = (v: EditorView) => {
      setLineFactsHandlers(v, {
        show: (line, anchor, via) => {
          if (returningGutterFocus) return;
          hideRemovedPreview();
          cancelHidePopover();
          clearTimeout(showPopoverTimer);
          if (via !== "pointer" || lineFactsCard()) showLineFacts(v, line, anchor, via === "enter");
          else showPopoverTimer = setTimeout(() => showLineFacts(v, line, anchor), 250);
        },
        leave: scheduleHidePopover,
        dismiss: hideLineFacts,
        refresh: () => {
          const held = lineFactsCard();
          if (!held) return;
          queueMicrotask(() => {
            if (lineFactsCard() !== held) return;
            const anchor = v.dom.querySelector<HTMLElement>(`.files-line-gutter__facts[data-line="${held.line}"]`)?.closest<HTMLElement>(".cm-gutterElement");
            if (anchor) showLineFacts(v, held.line, anchor, held.enter);
            else hideLineFacts();
          });
        },
      });
      setLineGutterProvenanceHandlers(v, {
        onActivate: (mark, anchor, line) => {
          const author = mark.contributors[0];
          if (mark.contributors.length === 1 && author) jumpToAttribution(author, line);
          else showLineFacts(v, line, anchor.closest<HTMLElement>(".cm-gutterElement") ?? anchor, true);
        },
      });
      setLineGutterFindingHandlers(v, {
        onActivate: mark => { const first = mark.findings[0]; if (first) openFindingRecord(first, mark.line); },
      });
      setLineGutterCutHandlers(v, {
        onShow: (fold, anchor, via) => {
          keepRemovedPreview();
          if (showRemovedTimer) clearTimeout(showRemovedTimer);
          if (via === "focus") {
            showRemovedPreview(v, fold, anchor);
            return;
          }
          showRemovedTimer = setTimeout(() => showRemovedPreview(v, fold, anchor), 110);
        },
        onHide: scheduleHideRemovedPreview,
        onOpen: (fold) => {
          hideRemovedPreview();
          openScopeDeletion(v, fold);
        },
      });
  };
  const observePresence = () => {
  createEffect(() => {
    agentPresenceFor(projectId());
    untrack(() => {
      const held = lineFactsCard();
      if (held && binding.view) showLineFacts(binding.view, held.line, held.anchor, held.enter);
    });
  });


  };
  const clearLineFactsTimers = () => {
    if (hidePopoverTimer) clearTimeout(hidePopoverTimer);
    if (showPopoverTimer) clearTimeout(showPopoverTimer);
  };
  return {
    lineFactsCard,
    cancelHidePopover,
    hideLineFacts,
    scheduleHidePopover,
    returnLineFactsFocus,
    removedPreview,
    hideRemovedPreview,
    keepRemovedPreview,
    scheduleHideRemovedPreview,
    openRemovedInPlace,
    rejectRemoved,
    jumpToAttribution,
    activateLineFact,
    findingDiagnosticHandlers,
    installGutterInteractions,
    observePresence,
    clearRemovedTimers,
    clearLineFactsTimers,
  };
}
