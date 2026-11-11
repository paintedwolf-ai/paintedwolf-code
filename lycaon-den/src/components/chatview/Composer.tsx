import { createComposerElevatedAccess, ElevatedAccessButton } from "./ComposerElevatedAccess.tsx";
import { createComposerVaultUnlock, VaultUnlockButton } from "./ComposerVaultUnlock.tsx";
import { createComposerEditMenu } from "./composer-edit-menu.ts";
import { Show, batch, createEffect, createSignal, onCleanup, untrack } from "solid-js";
import {
  attachDispatcher,
  declinable,
  registerCommandHandler,
  setAskSendArmed,
  setComposerFocused,
} from "../../shortcuts/dispatcher.ts";
import {
  activeElementInFocusRegion,
  isFocusRegionMounted,
  registerFocusRegion,
  releaseFocusRegion,
} from "../../shortcuts/focus-region.ts";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import {
  composerBlockReason,
  isComposerDisabled,
  isComposerSendBlocked,
  type ComposerBlockReason,
} from "../../chat/composer/composer-rules.ts";
import { composerDraftForSession, flushComposerDraftsToDisk } from "../../chat/composer/composer-drafts.ts";
import type { CoordinatorVisionSupport } from "../../chat/composer/composer-vision.ts";
import { clearRevealHighlight } from "../../chat/transcript/presentation/reveal-highlight.ts";
import {
  attachmentTurnBytes,
  attachPastedFiles,
  composerAttachmentCapabilities,
  rejectMessageForCode,
} from "../../chat/composer/composer-attachments.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import {
  acquireComposerDocumentLease,
  consumeComposerDocument,
  removeComposerDocumentAttachment,
  stageComposerMutation,
  updateComposerDocumentDraft,
} from "../../chat/composer/composer-document-store.ts";
import type { ChatDestination } from "../../chat/composer/shared-composer-document.ts";
import { promptPartsFromPendingAttachments } from "../../chat/composer/prompt-parts.ts";
import type {
  PromptAttachmentPart,
  PromptReferencePart,
  PromptSecretReferencePart,
  SessionStatus,
  TurnClock,
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import type { SidecarStatus } from "../../store/app-state-model.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { ComposerActivityIndicator } from "./ComposerActivityIndicator.tsx";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ExternalContentBadge } from "../nav/ExternalContentBadge.tsx";
import { createSettledVisibility } from "../../chat/composer/composer-status-settle.ts";
import { openInSearch } from "../../search/search-nav.ts";
import { exitRecall, recordSentPrompt } from "../../chat/composer/prompt-recall.ts";
import { externalContentSearchQuery } from "../../chat/untrusted/untrusted-content-copy.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { appBootPreparation } from "../../platform/connection/app-boot-readiness.ts";
import {
  INLINE_TEXT_TOO_LARGE_MESSAGE,
  inlineTextWithinLimit,
} from "../../chat/composer/large-text-paste.ts";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { ContextMenu } from "../ContextMenu.tsx";
import { MarkSecretDialog } from "../source/secrets/MarkSecretDialog.tsx";
import { useComposerAttachments } from "./useComposerAttachments.ts";
import { ComposerAttachmentStrip } from "./ComposerAttachmentStrip.tsx";
import { useComposerDraftSizing } from "./useComposerDraftSizing.ts";

let nextSlashListboxId = 0;

export type ComposerSendPayload = {
  recovery?: import("../../api/types.ts").PromptRecovery;
  text: string;
  attachments?: PromptAttachmentPart[];
  references?: PromptReferencePart[];
  secrets?: PromptSecretReferencePart[];
  /** Fires after the pending submission is visible. */
  onPendingSend?: (destination: "transcript" | "queue") => void;
  attachmentLabels?: readonly string[];
};

type Props = {
  sidecarStatus: SidecarStatus;
  approvalsRevision?: number;
  sessionId: string;
  projectId: string;
  chatHydrationLock?: string | null;
  /** `preparing` blocks sending until the workspace is ready. */
  sessionStatus?: SessionStatus | null;
  activeWorkflow?: WorkflowRun | null;
  workflowCatalog?: readonly WorkflowSummary[];
  placeholder?: string;
  /** The active or armed workflow accepts an empty submission. */
  emptySubmitEnabled?: boolean;
  /** Provider readiness blocks composing. */
  needsProvider?: boolean;
  pauseHint?: string | null;
  /** Host-reported image support for the active model. */
  visionSupport?: CoordinatorVisionSupport;
  /** False keeps the draft after submission. */
  onSend: (payload: ComposerSendPayload) => boolean | void | Promise<boolean | void>;
  /** A local prompt already has its pending transcript row. */
  pendingTranscriptSend?: boolean;
  streaming?: boolean;
  stopping?: boolean;
  onStop?: () => void | Promise<void>;
  /** An ask dock sends its selected answer. */
  askPending?: boolean;
  /** A staged ask choice arms Send. */
  askAnswerReady?: boolean;
  /** One pending approval controls Send. */
  approvalRedirect?: boolean;
  activityLabel?: string;
  turnClock?: TurnClock;
  externalContent?: boolean;
  focusWhen?: string | null;
  refocusPulse?: number;
  /** Explicit document replacements refresh the mounted draft. */
  rehydrateDraftPulse?: number;
  /** False while this composer lives on a hidden resident chat. */
  claimFocus?: boolean;
};

function focusComposerInput(el: HTMLTextAreaElement | undefined): void {
  if (!el || el.disabled || document.activeElement === el) return;
  // Foreground surfaces keep their focus claim.
  if (isFocusRegionMounted("settings") || isFocusRegionMounted("find")) return;
  if (activeElementInFocusRegion("composer")) return;
  el.focus({ preventScroll: true });
}

function scheduleComposerFocus(el: HTMLTextAreaElement | undefined): void {
  queueMicrotask(() => focusComposerInput(el));
}

export function Composer(props: Props) {
  const destination = (): ChatDestination => ({
    projectId: props.projectId.trim(),
    sessionId: props.sessionId.trim(),
  });
  const destinationKey = (target: ChatDestination = destination()) =>
    `${target.projectId}\0${target.sessionId}`;

  const blockReason = (): ComposerBlockReason =>
    composerBlockReason(
      props.sidecarStatus,
      props.sessionId,
      props.activeWorkflow,
      props.chatHydrationLock,
      props.needsProvider,
      props.sessionStatus,
    );

  /** Send-only holds still allow draft edits. */
  const composeBlocked = () =>
    isComposerDisabled(
      props.sidecarStatus,
      props.sessionId,
      props.activeWorkflow,
      props.chatHydrationLock,
      props.needsProvider,
      props.sessionStatus,
    );

  const sendBlocked = () =>
    !appBootPreparation.ready() ||
    isComposerSendBlocked(
      props.sidecarStatus,
      props.sessionId,
      props.activeWorkflow,
      props.chatHydrationLock,
      props.needsProvider,
      props.sessionStatus,
    );

  const placeholder = () => {
    // Blocking state takes precedence over contextual placeholders.
    if (blockReason() === "no_provider") {
      return "Add a model provider in Settings → AI providers to start chatting";
    }
    if (!appBootPreparation.ready() || blockReason() === "session_preparing") {
      return "Opening chat…";
    }
    if (props.placeholder) return props.placeholder;
    if (blockReason() === "workflow_paused") {
      return "Workflow paused — resume to continue chatting";
    }
    return "Message…";
  };

  const [text, setText] = createSignal(composerDraftForSession(props.sessionId));
  const slashListboxId = `den-composer-slash-${++nextSlashListboxId}`;
  const [composerErrors, setComposerErrors] = createSignal<Record<string, string>>({});
  const composerError = () => composerErrors()[destinationKey()] ?? null;
  const setComposerError = (target: ChatDestination, message: string | null) => {
    const targetKey = destinationKey(target);
    setComposerErrors((previous) => {
      if (message === null && previous[targetKey] === undefined) return previous;
      const next = { ...previous };
      if (message === null) delete next[targetKey];
      else next[targetKey] = message;
      return next;
    });
  };

  let inputRef: HTMLTextAreaElement | undefined;
  let footerRef: HTMLElement | undefined;
  let bodyRef: HTMLDivElement | undefined;
  let scrollRef: HTMLDivElement | undefined;
  let viewportRef: HTMLDivElement | undefined;
  let fileInputRef: HTMLInputElement | undefined;

  const {
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
    dismissSlashSuggestions,
    applySlashSuggestion,
    onSlashSuggestionKeyDown,
  } = useComposerDraftSizing({
    sessionId: () => props.sessionId,
    destination,
    composeBlocked,
    workflowCatalog: () => props.workflowCatalog,
    focusWhen: () => props.focusWhen,
    refocusPulse: () => props.refocusPulse,
    rehydrateDraftPulse: () => props.rehydrateDraftPulse,
    text,
    setText,
    setComposerError,
    inputRef: () => inputRef,
    footerRef: () => footerRef,
    bodyRef: () => bodyRef,
    scrollRef: () => scrollRef,
    viewportRef: () => viewportRef,
    focusInput: () => focusComposerInput(inputRef),
    scheduleFocus: () => scheduleComposerFocus(inputRef),
  });

  const {
    attachments,
    pastedTextAttachments,
    visiblePastedTextUploads,
    pasteAnnouncement,
    sendableAttachments,
    hasRejectChips,
    hasPastedTextUploads,
    hasByteAttachments,
    visionHint,
    clearAttachments,
    interceptTextPaste,
    addFiles,
    removeAttachment,
    isAttachLimitReached,
  } = useComposerAttachments({
    sessionId: () => props.sessionId,
    destination,
    visionSupport: () => props.visionSupport,
    currentText: () => inputRef?.value ?? text(),
    setComposerError,
    scheduleFocus: () => scheduleComposerFocus(inputRef),
  });

  const {
    editMenuAnchor,
    setEditMenuAnchor,
    editMenuRetainsFocus,
    markSecretTarget,
    setMarkSecretTarget,
    editContextMenuItems,
    openEditContextMenu,
    clearSecretSelection,
  } = createComposerEditMenu({
    input: () => inputRef,
    text,
    sessionId: () => props.sessionId,
    projectId: () => props.projectId,
    destination,
    removeMarkedDraftRange,
    growDraft,
  });

  const elevated = createComposerElevatedAccess({
    client: () => {
      props.sidecarStatus;
      return getLycaonClient();
    },
    connected: () => props.sidecarStatus === "connected",
    sessionId: () => props.sessionId,
    revision: () => `${props.projectId}:${props.approvalsRevision ?? 0}:${props.sidecarStatus}`,
    focus: () => inputRef?.focus({ preventScroll: true }),
  });
  const vaultUnlock = createComposerVaultUnlock({
    client: () => {
      props.sidecarStatus;
      return getLycaonClient();
    },
    connected: () => props.sidecarStatus === "connected",
    sessionId: () => props.sessionId,
    revision: () => `${props.projectId}:${props.sidecarStatus}`,
  });

  // Plain prompts overlap; decision sends serialize.
  const pendingSubmissions = new Map<string, number>();
  // Each draft maps to one active submission.
  const inFlightDrafts = new Map<string, Set<string>>();
  const [pendingSubmissionVersion, setPendingSubmissionVersion] = createSignal(0);
  const submitPending = () => {
    pendingSubmissionVersion();
    return (pendingSubmissions.get(destinationKey()) ?? 0) > 0;
  };
  const bumpSubmitPending = (target: ChatDestination, delta: 1 | -1) => {
    const targetKey = destinationKey(target);
    const next = (pendingSubmissions.get(targetKey) ?? 0) + delta;
    if (next <= 0) pendingSubmissions.delete(targetKey);
    else pendingSubmissions.set(targetKey, next);
    setPendingSubmissionVersion((version) => version + 1);
  };

  // Distinguishes this composer from other focus claimants.
  const focusClaim = {};
  const [composerEl, setComposerEl] = createSignal<HTMLTextAreaElement | undefined>();
  createEffect(() => {
    const el = composerEl();
    if (!el || props.claimFocus === false) {
      releaseFocusRegion("composer", focusClaim);
      return;
    }
    registerFocusRegion("composer", el, focusClaim);
  });

  const sending = () => Boolean(props.pendingTranscriptSend && !props.streaming);
  const liveDesired = () => Boolean((props.streaming || sending()) && props.onStop);
  const externalDesired = () => Boolean(props.externalContent);
  const liveSettled = createSettledVisibility(liveDesired);
  const externalSettled = createSettledVisibility(externalDesired);
  const hasDraftContent = () =>
    text().trim().length > 0 ||
    sendableAttachments().length > 0 ||
    Boolean(props.askAnswerReady);
  const hasSendableDraft = () =>
    hasDraftContent() || Boolean(props.emptySubmitEnabled);
  const submitHeld = () => sendBlocked() || hasRejectChips() || hasPastedTextUploads();
  const canSubmit = () => !submitHeld() && hasSendableDraft();
  // An armed empty submit does not displace Stop.
  const showStopAsPrimary = () =>
    liveDesired() &&
    (Boolean(props.stopping) ||
      submitHeld() ||
      !hasDraftContent() ||
      submitPending());
  /** A staged ask selection arms Send with an empty draft. */
  const sendArmedByAsk = () =>
    Boolean(props.askPending && props.askAnswerReady) && !sendBlocked();
  /** Each decision accepts one submission at a time. */
  const decisionSend = () => Boolean(props.askPending || props.approvalRedirect);
  const stopTitle = () =>
    props.stopping
      ? "Stopping…"
      : `Stop (${bindingForHandler("session.stop", { overrides: shortcutOverrides() })})`;

  const presence = useResidentPresence();
  const [inputFocused, setInputFocused] = createSignal(false);
  /** A staged choice keeps focus on its control, so it arms Enter for the displayed chat only. */
  const holdsAskSend = () => presence() === "active" && sendArmedByAsk();
  createEffect(() => {
    setAskSendArmed(holdsAskSend(), focusClaim);
    onCleanup(() => setAskSendArmed(false, focusClaim));
  });

  const openExternalContentSearch = () => {
    const projectId = props.projectId.trim();
    const sessionId = props.sessionId.trim();
    const query = externalContentSearchQuery(sessionId);
    if (!query) return;
    openInSearch(projectId, query);
  };

  const submit = async () => {
    const submittedDestination = destination();
    const submittedKey = destinationKey(submittedDestination);
    if (decisionSend() && (pendingSubmissions.get(submittedKey) ?? 0) > 0) {
      return;
    }
    if (inFlightDrafts.get(submittedKey)?.has(text())) return;
    const value = text().trim();
    const submittedDraft = text();
    const pending = sendableAttachments();
    const submittedAttachmentIDs = attachments().map((attachment) => attachment.id).join("\0");
    // A ready ask selection sends on its own; the message field may stay empty.
    if (
      (!value && pending.length === 0 && !props.askAnswerReady && !props.emptySubmitEnabled) ||
      sendBlocked()
    ) {
      return;
    }
    if (hasRejectChips()) {
      setComposerError(submittedDestination, "Remove unsupported attachments before sending");
      return;
    }
    if (!inlineTextWithinLimit(value, composerAttachmentCapabilities())) {
      setComposerError(submittedDestination, INLINE_TEXT_TOO_LARGE_MESSAGE);
      return;
    }
    const pendingBytes = attachmentTurnBytes(pending);
    // Restored chips defer unknown limits to host validation at send time.
    const capabilities = composerAttachmentCapabilities();
    if (
      pendingBytes > 0 &&
      capabilities &&
      (capabilities.max_turn_bytes <= 0 || pendingBytes > capabilities.max_turn_bytes)
    ) {
      setComposerError(submittedDestination, rejectMessageForCode("attachment_too_large"));
      return;
    }
    clearRevealHighlight();
    const { attachments: attachParts, references, secrets } =
      promptPartsFromPendingAttachments(pending);
    // Release only the submitted draft and attachments.
    let eagerCleared = false;
    const eagerClear = () => {
      if (eagerCleared) return;
      eagerCleared = true;
      recordSentPrompt(submittedDestination.sessionId, value);
      if (
        composerDraftForSession(submittedDestination.sessionId) === submittedDraft
      ) {
        updateComposerDocumentDraft(submittedDestination, "");
        void flushComposerDraftsToDisk();
        if (props.sessionId === submittedDestination.sessionId) {
          replaceText("");
          clearDraft();
        }
      }
      for (const attachment of pending) {
        void removeComposerDocumentAttachment(submittedDestination, attachment.id);
      }
      setComposerError(submittedDestination, null);
    };
    // Restore a failed send only when no newer input exists.
    const restoreDraft = () => {
      if (!eagerCleared) return;
      if (
        value &&
        composerDraftForSession(submittedDestination.sessionId) === ""
      ) {
        updateComposerDocumentDraft(submittedDestination, submittedDraft);
        if (props.sessionId === submittedDestination.sessionId) {
          replaceText(submittedDraft);
          queueMicrotask(growDraft);
        }
      }
      if (pending.length > 0) {
        void stageComposerMutation(submittedDestination, pending);
      }
    };
    bumpSubmitPending(submittedDestination, 1);
    const inFlight = inFlightDrafts.get(submittedKey) ?? new Set<string>();
    inFlight.add(submittedDraft);
    inFlightDrafts.set(submittedKey, inFlight);
    try {
      const keepDraft =
        (await props.onSend({
          text: value,
          attachments: attachParts.length > 0 ? attachParts : undefined,
          references: references.length > 0 ? references : undefined,
          secrets: secrets.length > 0 ? secrets : undefined,
          onPendingSend: eagerClear,
          attachmentLabels:
            pending.length > 0 ? pending.map((a) => a.name) : undefined,
        })) === false;
      if (keepDraft) {
        restoreDraft();
        return;
      }
      if (eagerCleared) return;

      // Non-prompt sends clear after resolution.
      recordSentPrompt(submittedDestination.sessionId, value);
      const draftUnchanged =
        composerDraftForSession(submittedDestination.sessionId) === submittedDraft;
      const attachmentsUnchanged =
        pendingAttachmentsForSession(submittedDestination.sessionId)
          .map((attachment) => attachment.id)
          .join("\0") === submittedAttachmentIDs;
      if (!draftUnchanged || !attachmentsUnchanged) return;

      if (props.sessionId === submittedDestination.sessionId) replaceText("");
      void consumeComposerDocument(submittedDestination).catch((error) => {
        setComposerError(
          submittedDestination,
          error instanceof Error
            ? error.message
            : "Message sent, but its staged composer state could not be cleared.",
        );
      });
      setComposerError(submittedDestination, null);
      if (props.sessionId === submittedDestination.sessionId) clearDraft();
    } catch {
      restoreDraft();
      setComposerError(submittedDestination, "Message could not be sent. Try again.");
    } finally {
      inFlight.delete(submittedDraft);
      if (inFlight.size === 0) inFlightDrafts.delete(submittedKey);
      bumpSubmitPending(submittedDestination, -1);
    }
  };

  // Composer-scoped commands belong to the composer whose input holds focus.
  createEffect(() => {
    if (!inputFocused() && !holdsAskSend()) return;
    onCleanup(
      registerCommandHandler("composer.send", () => {
        void submit();
      }),
    );
  });
  createEffect(() => {
    if (!inputFocused()) return;
    onCleanup(
      registerCommandHandler("composer.newline", () => {
        insertNewline();
      }),
    );
    onCleanup(
      registerCommandHandler(
        "composer.recallPrev",
        declinable(() => walkPromptRecall("older")),
      ),
    );
    onCleanup(
      registerCommandHandler(
        "composer.recallNext",
        declinable(() => walkPromptRecall("newer")),
      ),
    );
  });
  const detachListener = attachDispatcher();
  onCleanup(() => {
    detachListener();
    setComposerFocused(false, focusClaim);
    setAskSendArmed(false, focusClaim);
    releaseFocusRegion("composer", focusClaim);
  });

  return (
    <>
      <footer class="den-composer" ref={footerRef}>
        <div class="den-composer-body" ref={bodyRef}>
          <ComposerActivityIndicator
            present={liveSettled()}
            activityLabel={sending() ? "Sending" : props.activityLabel}
            turnClock={sending() ? undefined : props.turnClock}
            resize={resizeChrome}
          />
          {/* Drafted approval guidance arms the redirect controls together. */}
          <Show when={props.approvalRedirect && text().trim().length > 0}>
            <p class="den-composer-pause-hint" role="status" data-testid="composer-approval-redirect">
              <strong>{APPROVALS_COPY.card.composerRedirect.title}</strong>
              {" — "}
              {APPROVALS_COPY.card.composerRedirect.detail}
            </p>
          </Show>
          <Show when={props.pauseHint}>
            <p class="den-composer-pause-hint" role="status" data-testid="composer-pause-hint">
              {props.pauseHint}
            </p>
          </Show>
          <Show when={visionHint()}>
            <p class="den-composer-pause-hint" role="status" data-testid="composer-vision-hint">
              {visionHint()}
            </p>
          </Show>
          <Show when={composerError()}>
            <p class="den-composer-pause-hint" role="status" data-testid="composer-attach-error">
              {composerError()}
            </p>
          </Show>
          <span class="sr-only" role="status" aria-live="polite" aria-atomic="true">
            {pasteAnnouncement()}
          </span>
          <ComposerAttachmentStrip
            attachments={attachments()}
            visiblePastedTextUploads={visiblePastedTextUploads()}
            hasByteAttachments={hasByteAttachments()}
            onClearAttachments={clearAttachments}
            onRetryPastedUpload={(upload) => void pastedTextAttachments.retry(upload)}
            onRemovePastedUpload={(id) => pastedTextAttachments.remove(id)}
            onRemoveAttachment={removeAttachment}
            showSlashSuggestions={showSlashSuggestions()}
            slashListboxId={slashListboxId}
            slashSuggestions={slashSuggestions()}
            activeSlashSuggestion={activeSlashSuggestion()}
            anchor={() => footerRef}
            onDismissSlash={dismissSlashSuggestions}
            onSelectSlashIndex={setActiveSlashSuggestion}
            onApplySlashSuggestion={applySlashSuggestion}
          />
          <div class="den-composer-row">
            <Scrollport class="den-composer-scroll"
              ref={(el) => {
                scrollRef = el;
              }}
              viewportRef={(el) => {
                viewportRef = el;
              }}
            >
              <textarea
                ref={(el) => {
                  inputRef = el;
                  setComposerEl(el);
                  if (el) el.value = untrack(text);
                }}
                class="den-composer-input"
                placeholder={placeholder()}
                rows={2}
                disabled={composeBlocked()}
                onInput={(e) => {
                  // Editing recalled text exits recall mode.
                  exitRecall(props.sessionId);
                  const value = e.currentTarget.value;
                  if (!inlineTextWithinLimit(value, composerAttachmentCapabilities())) {
                    replaceText(text());
                    setComposerError(destination(), INLINE_TEXT_TOO_LARGE_MESSAGE);
                    growDraft();
                    return;
                  }
                  batch(() => {
                    setText(value);
                    updateComposerDocumentDraft(destination(), value);
                  });
                  growDraft();
                  revealCaret();
                }}
                onClick={() => revealCaret()}
                onKeyUp={(event) => {
                  if (
                    event.key === "ArrowUp" ||
                    event.key === "ArrowDown" ||
                    event.key === "ArrowLeft" ||
                    event.key === "ArrowRight" ||
                    event.key === "Home" ||
                    event.key === "End" ||
                    event.key === "PageUp" ||
                    event.key === "PageDown"
                  ) {
                    revealCaret();
                  }
                }}
                onFocus={() => {
                  setInputFocused(true);
                  setComposerFocused(true, focusClaim);
                  void acquireComposerDocumentLease(destination());
                }}
                onBlur={() => {
                  setInputFocused(false);
                  setComposerFocused(false, focusClaim);
                }}
                onBeforeInput={(e) => {
                  const input = e as InputEvent;
                  if (
                    input.inputType === "insertFromPaste" &&
                    input.data &&
                    interceptTextPaste(
                      input.data,
                      e.currentTarget.selectionStart,
                      e.currentTarget.selectionEnd,
                    )
                  ) {
                    e.preventDefault();
                  }
                }}
                onPaste={(e) => {
                  if (
                    attachPastedFiles(e, addFiles, (error) =>
                      setComposerError(
                        destination(),
                        error instanceof Error ? error.message : String(error),
                      ),
                    )
                  ) {
                    return;
                  }
                  const pasted = e.clipboardData?.getData("text/plain") ?? "";
                  if (
                    interceptTextPaste(
                      pasted,
                      e.currentTarget.selectionStart,
                      e.currentTarget.selectionEnd,
                    )
                  ) {
                    e.preventDefault();
                  }
                }}
                onContextMenu={(event) => openEditContextMenu(event)}
                onKeyDown={(event) => {
                  if (event.key === "F10" && event.shiftKey && openEditContextMenu()) {
                    event.preventDefault();
                    return;
                  }
                  onSlashSuggestionKeyDown(event);
                }}
                aria-autocomplete={showSlashSuggestions() ? "list" : undefined}
                aria-expanded={showSlashSuggestions()}
                aria-controls={showSlashSuggestions() ? slashListboxId : undefined}
                aria-activedescendant={
                  showSlashSuggestions()
                    ? `${slashListboxId}-option-${activeSlashSuggestion()}`
                    : undefined
                }
                data-testid="chat-composer"
              />
            </Scrollport>
            <div class="den-composer-action-pad">
              <input
                ref={fileInputRef}
                type="file"
                accept="*/*"
                multiple
                hidden
                data-testid="composer-file-input"
                onChange={(e) => {
                  const files = Array.from(e.currentTarget.files ?? []);
                  e.currentTarget.value = "";
                  void addFiles(files);
                }}
              />
              <div
                class="den-composer-status-cluster"
                data-testid="composer-status-cluster"
                {...chromeProps()}
              >
                <Show when={externalSettled()}>
                  <div
                    class="den-composer-status-item"
                    data-status-id="external"
                    data-testid="composer-external-content-cell"
                  >
                    <ExternalContentBadge visible onActivate={openExternalContentSearch} />
                  </div>
                </Show>
                <ElevatedAccessButton state={elevated} />
                <VaultUnlockButton state={vaultUnlock} />
              </div>
              <button
                type="button"
                class="den-quiet-icon-btn"
                data-testid="composer-attach"
                disabled={composeBlocked() || isAttachLimitReached()}
                onClick={() => fileInputRef?.click()}
                aria-label="Attach file"
                data-tip="Attach file"
              >
                <ThemeIcon slot="plus" size={16} />
              </button>
              <Show
                when={showStopAsPrimary()}
                fallback={
                  <button
                    type="button"
                    class="den-composer-send den-quiet-icon-btn"
                    classList={{ "den-composer-send--armed": sendArmedByAsk() }}
                    data-armed={sendArmedByAsk() ? "" : undefined}
                    data-testid="composer-send"
                    disabled={(decisionSend() && submitPending()) || !canSubmit()}
                    onClick={() => void submit()}
                    aria-label={
                      props.askPending
                        ? "Send answer"
                        : props.streaming
                          ? "Queue message"
                          : "Send message"
                    }
                    data-tip={
                      !sendArmedByAsk() && !props.askPending && props.streaming
                        ? "Queue for the next turn"
                        : undefined
                    }
                  >
                    <ThemeIcon slot="arrow-up" size={16} class="den-composer-send-icon" />
                  </button>
                }
              >
                <button
                  type="button"
                  class="den-composer-stop den-quiet-icon-btn"
                  data-testid="stop-coordinator"
                  disabled={props.stopping}
                  aria-busy={props.stopping ? "true" : undefined}
                  onClick={() => {
                    if (!props.stopping) void props.onStop?.();
                  }}
                  aria-label={props.stopping ? "Stopping response" : "Stop response"}
                  data-tip={stopTitle()}
                >
                  <ThemeIcon slot="stop" size={16} />
                </button>
              </Show>
            </div>
          </div>
        </div>
      </footer>
      <Show when={editMenuAnchor()} keyed>
        {(anchor) => (
          <ContextMenu
            anchor={anchor}
            items={editContextMenuItems()}
            retainFocus={editMenuRetainsFocus()}
            onDismiss={() => setEditMenuAnchor(null)}
          />
        )}
      </Show>
      <MarkSecretDialog
        target={markSecretTarget()}
        onClose={() => setMarkSecretTarget(null)}
        onMarked={() => {
          clearSecretSelection();
          setComposerError(destination(), null);
        }}
        onError={(message) => setComposerError(destination(), message)}
      />
    </>
  );
}
