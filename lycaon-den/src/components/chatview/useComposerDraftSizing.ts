import { batch, createEffect, createMemo, createSignal, on, onCleanup, onMount } from "solid-js";
import type { WorkflowSummary } from "../../api/types.ts";
import { composerDraftForSession, flushComposerDraftsToDisk } from "../../chat/composer/composer-drafts.ts";
import { filterWorkflowSlashSuggestions } from "../../chat/composer/composer-workflow-triggers.ts";
import { composerAttachmentCapabilities } from "../../chat/composer/composer-attachments.ts";
import {
  ensureComposerDocument,
  updateComposerDocumentDraft,
} from "../../chat/composer/composer-document-store.ts";
import type { ChatDestination } from "../../chat/composer/shared-composer-document.ts";
import {
  caretAtRecallEdge,
  inRecall,
  walkRecall,
} from "../../chat/composer/prompt-recall.ts";
import {
  INLINE_TEXT_TOO_LARGE_MESSAGE,
  inlineTextWithinLimit,
} from "../../chat/composer/large-text-paste.ts";
import { handleSlashSuggestionKey } from "../../chat/composer/slash-suggestion-keys.ts";
import { isEditableTarget } from "../../shortcuts/dispatcher.ts";
import { syncThemedScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { revealTextareaCaret } from "../../platform/scrolling/textarea-caret-reveal.ts";
import {
  HEIGHT_TOGGLE_TIMING,
  followBodyHeight,
  type HeightFollow,
  type HeightMotionTiming,
} from "../../ui/height-toggle-motion.ts";

// Early deceleration keeps resizing responsive during typing.
const DRAFT_RESIZE_TIMING: HeightMotionTiming = {
  duration: 20,
  easing: "cubic-bezier(0.2, 0, 0, 1)",
};

/** Returns whether the draft height changed. */
export function fitDraftHeight(
  el: HTMLTextAreaElement,
  scrollHost: HTMLElement | undefined,
): boolean {
  const previous = el.style.height;
  el.style.height = "auto";
  const next = `${el.scrollHeight}px`;
  el.style.height = next;
  el.style.overflowY = "hidden";
  // Only a shorter draft can leave the scroll host past its content.
  if (scrollHost && !(Number.parseFloat(next) >= Number.parseFloat(previous))) {
    syncThemedScrollbar(scrollHost);
  }
  return next !== previous;
}

export function clearDraftHeight(
  el: HTMLTextAreaElement,
  scrollHost: HTMLElement | undefined,
): boolean {
  const previous = el.style.height;
  el.style.height = "auto";
  el.style.overflowY = "hidden";
  if (scrollHost) syncThemedScrollbar(scrollHost);
  return previous !== "auto";
}

export type UseComposerDraftSizingOptions = {
  sessionId: () => string;
  destination: () => ChatDestination;
  composeBlocked: () => boolean;
  workflowCatalog: () => readonly WorkflowSummary[] | undefined;
  focusWhen: () => string | null | undefined;
  refocusPulse: () => number | undefined;
  rehydrateDraftPulse: () => number | undefined;
  text: () => string;
  setText: (value: string) => void;
  setComposerError: (target: ChatDestination, message: string | null) => void;
  inputRef: () => HTMLTextAreaElement | undefined;
  footerRef: () => HTMLElement | undefined;
  bodyRef: () => HTMLDivElement | undefined;
  scrollRef: () => HTMLDivElement | undefined;
  viewportRef: () => HTMLDivElement | undefined;
  focusInput: () => void;
  scheduleFocus: () => void;
};

export function useComposerDraftSizing(options: UseComposerDraftSizingOptions) {
  const [dismissedSlashText, setDismissedSlashText] = createSignal<string | null>(null);
  const [activeSlashSuggestion, setActiveSlashSuggestion] = createSignal(0);
  let composerHeight: HeightFollow | undefined;
  let resolvedDocumentKey = "";

  const replaceText = (value: string) => {
    options.setText(value);
    const el = options.inputRef();
    // Unchanged values preserve the live selection.
    if (el && el.value !== value) el.value = value;
  };

  const sizeDraft = (size: typeof fitDraftHeight, motion: "ease" | "snap") => {
    const el = options.inputRef();
    if (!el) return;
    const apply = () => size(el, options.scrollRef());
    if (!composerHeight) apply();
    else if (motion === "snap") composerHeight.snap(apply);
    else composerHeight.change(DRAFT_RESIZE_TIMING, apply);
  };

  /** Drafts scroll once the composer reaches its height cap. */
  const growDraft = () => sizeDraft(fitDraftHeight, "ease");
  const clearDraft = () => sizeDraft(clearDraftHeight, "snap");
  /** Conversation switches resize the draft without animation. */
  const snapDraft = () => sizeDraft(fitDraftHeight, "snap");
  const revealCaret = () => {
    const el = options.inputRef();
    const vp = options.viewportRef();
    if (el && vp) revealTextareaCaret(el, vp);
  };

  const resizeChrome = (mutate: () => void) => {
    if (composerHeight) composerHeight.change(HEIGHT_TOGGLE_TIMING, mutate);
    else mutate();
  };

  onMount(() => {
    const footer = options.footerRef();
    const body = options.bodyRef();
    if (!footer || !body) return;
    const follow = followBodyHeight(footer, body);
    composerHeight = follow;
    snapDraft();
    onCleanup(() => {
      if (composerHeight === follow) composerHeight = undefined;
      follow.dispose();
    });
  });

  /** Native deletion preserves undo for marked text. */
  const removeMarkedDraftRange = (
    start: number,
    end: number,
    nextDraft: string,
  ) => {
    const el = options.inputRef();
    if (el && typeof document.execCommand === "function") {
      el.focus();
      el.setSelectionRange(start, end);
      if (document.execCommand("delete") && el.value === nextDraft) {
        options.setText(nextDraft);
        return;
      }
    }
    replaceText(nextDraft);
  };

  const writeDraft = (value: string): boolean => {
    const target = options.destination();
    if (!inlineTextWithinLimit(value, composerAttachmentCapabilities())) {
      options.setComposerError(target, INLINE_TEXT_TOO_LARGE_MESSAGE);
      return false;
    }
    batch(() => {
      replaceText(value);
      updateComposerDocumentDraft(target, value);
    });
    return true;
  };

  // Multiline recall yields arrow keys to the caret.
  const walkPromptRecall = (direction: "older" | "newer"): boolean => {
    if (options.composeBlocked()) return false;
    const sessionId = options.sessionId();
    if (options.text().length > 0 && !inRecall(sessionId)) return false;
    const el = options.inputRef();
    if (
      el &&
      !caretAtRecallEdge(
        el.value,
        el.selectionStart,
        el.selectionEnd,
        direction,
      )
    ) {
      return false;
    }
    const recalled = walkRecall(sessionId, direction);
    if (recalled === null) return false;
    writeDraft(recalled);
    queueMicrotask(() => {
      const input = options.inputRef();
      if (!input) return;
      growDraft();
      input.setSelectionRange(input.value.length, input.value.length);
      revealCaret();
    });
    return true;
  };

  const insertNewline = () => {
    const el = options.inputRef();
    if (!el || options.composeBlocked()) return;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const next = `${el.value.slice(0, start)}\n${el.value.slice(end)}`;
    if (!writeDraft(next)) return;
    queueMicrotask(() => {
      el.selectionStart = el.selectionEnd = start + 1;
      growDraft();
      revealCaret();
    });
  };

  const slashSuggestions = createMemo(() =>
    filterWorkflowSlashSuggestions(options.text(), options.workflowCatalog() ?? []),
  );
  const showSlashSuggestions = () =>
    !options.composeBlocked() &&
    options.text().trimStart().startsWith("/") &&
    slashSuggestions().length > 0 &&
    dismissedSlashText() !== options.text();

  createEffect(() => {
    const count = slashSuggestions().length;
    if (!showSlashSuggestions() || count === 0) {
      setActiveSlashSuggestion(0);
      return;
    }
    setActiveSlashSuggestion((index) => Math.min(index, count - 1));
  });

  const applySlashSuggestion = (trigger: string) => {
    writeDraft(`${trigger} `);
    setDismissedSlashText(options.text());
    queueMicrotask(() => {
      growDraft();
      options.focusInput();
      revealCaret();
    });
  };

  const onSlashSuggestionKeyDown = (event: KeyboardEvent) => {
    if (!showSlashSuggestions()) return;
    const suggestions = slashSuggestions();
    handleSlashSuggestionKey(event, {
      count: suggestions.length,
      active: activeSlashSuggestion,
      setActive: setActiveSlashSuggestion,
      choose: (index) => {
        const suggestion = suggestions[index];
        if (suggestion) applySlashSuggestion(suggestion.trigger);
      },
      dismiss: () => setDismissedSlashText(options.text()),
    });
  };

  // Peer views share one host-managed chat document.
  createEffect(() => {
    const target = options.destination();
    const targetKey = `${target.projectId}\0${target.sessionId}`;
    if (targetKey === resolvedDocumentKey) return;
    resolvedDocumentKey = targetKey;
    void ensureComposerDocument(target);
  });

  // Mirror explicit shared-document updates into the mounted field.
  createEffect(() => {
    const stored = composerDraftForSession(options.sessionId());
    if (stored === options.text()) return;
    replaceText(stored);
    queueMicrotask(growDraft);
  });

  createEffect(
    on(
      options.sessionId,
      (sessionId, prevSessionId) => {
        if (prevSessionId) void flushComposerDraftsToDisk();
        replaceText(composerDraftForSession(sessionId));
        queueMicrotask(snapDraft);
      },
      { defer: true },
    ),
  );

  createEffect(
    on(options.focusWhen, (focusWhen) => {
      if (!focusWhen || options.composeBlocked()) return;
      options.scheduleFocus();
    }),
  );

  // Unblocking returns the caret unless another text field holds it.
  createEffect(
    on(options.composeBlocked, (isDisabled, wasDisabled) => {
      if (isDisabled || wasDisabled !== true || !options.focusWhen()) return;
      if (isEditableTarget(document.activeElement)) return;
      options.scheduleFocus();
    }),
  );

  createEffect(
    on(
      options.refocusPulse,
      () => {
        if (options.composeBlocked()) return;
        options.scheduleFocus();
      },
      { defer: true },
    ),
  );

  createEffect(
    on(
      options.rehydrateDraftPulse,
      () => {
        replaceText(composerDraftForSession(options.sessionId()));
        queueMicrotask(() => {
          const el = options.inputRef();
          if (!el) return;
          growDraft();
          // Explicit edit preloads place the caret after the draft.
          el.setSelectionRange(el.value.length, el.value.length);
        });
      },
      { defer: true },
    ),
  );

  return {
    replaceText,
    growDraft,
    clearDraft,
    revealCaret,
    resizeChrome,
    removeMarkedDraftRange,
    walkPromptRecall,
    insertNewline,
    slashSuggestions,
    showSlashSuggestions,
    activeSlashSuggestion,
    setActiveSlashSuggestion,
    dismissSlashSuggestions: () => setDismissedSlashText(options.text()),
    applySlashSuggestion,
    onSlashSuggestionKeyDown,
  };
}
