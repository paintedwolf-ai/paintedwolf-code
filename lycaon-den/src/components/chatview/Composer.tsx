import { createComposerElevatedAccess, ElevatedAccessButton } from "./ComposerElevatedAccess.tsx";
import { createComposerVaultUnlock, VaultUnlockButton } from "./ComposerVaultUnlock.tsx";
import { createComposerEditMenu } from "./composer-edit-menu.ts";
import { For, Show, batch, createEffect, createMemo, createSignal, on, onCleanup, onMount, untrack } from "solid-js";
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
import { filterWorkflowSlashSuggestions } from "../../chat/composer/composer-workflow-triggers.ts";
import type { CoordinatorVisionSupport } from "../../chat/composer/composer-vision.ts";
import { clearRevealHighlight } from "../../chat/transcript/presentation/reveal-highlight.ts";
import {
  ATTACHMENT_PROVIDER_SEND_HINT,
  composerAttachmentCapabilities,
  attachmentChipDetail,
  attachmentChipLabel,
  attachmentKindGlyph,
  attachmentPreviewSnippet,
  attachmentTurnBytes,
  countsAsImage,
  fileToComposerAttachment,
  filesFromClipboard,
  formatAttachmentBytes,
  isAllowedImageMime,
  isAllowedVideoMime,
  isByteAttachmentKind,
  isPayloadAttachment,
  isSendablePendingAttachment,
  rejectMessageForCode,
  type ComposerPendingAttachment,
} from "../../chat/composer/composer-attachments.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import {
  acquireComposerDocumentLease,
  clearComposerDocumentAttachments,
  consumeComposerDocument,
  ensureComposerDocument,
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
  WorkflowRun,
  WorkflowSummary,
} from "../../api/types.ts";
import type { TurnClock } from "../../api/types.ts";
import type { SidecarStatus } from "../../store/app-state-model.ts";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { shortcutOverrides } from "../../settings/system/shortcut-prefs.ts";
import { ComposerActivityIndicator } from "./ComposerActivityIndicator.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { ExternalContentBadge } from "../nav/ExternalContentBadge.tsx";
import { createSettledVisibility } from "../../chat/composer/composer-status-settle.ts";
import { openInSearch } from "../../search/search-nav.ts";
import {
  caretAtRecallEdge,
  exitRecall,
  inRecall,
  recordSentPrompt,
  walkRecall,
} from "../../chat/composer/prompt-recall.ts";
import { externalContentSearchQuery } from "../../chat/untrusted/untrusted-content-copy.ts";
import { syncThemedScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { appBootPreparation } from "../../platform/connection/app-boot-readiness.ts";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import {
  INLINE_TEXT_TOO_LARGE_MESSAGE,
  inlineTextWithinLimit,
  shouldAttachTextPaste,
} from "../../chat/composer/large-text-paste.ts";
import { createPastedTextAttachmentController } from "../../chat/composer/pasted-text-attachment-controller.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { ContextMenu } from "../ContextMenu.tsx";
import { MarkSecretDialog } from "../source/secrets/MarkSecretDialog.tsx";
import {
  HEIGHT_TOGGLE_TIMING,
  followBodyHeight,
  type HeightFollow,
  type HeightMotionTiming,
} from "../../ui/height-toggle-motion.ts";

let nextSlashListboxId = 0;

export type ComposerSendPayload = {
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

// Early deceleration keeps resizing responsive during typing.
const DRAFT_RESIZE_TIMING: HeightMotionTiming = {
  duration: 20,
  easing: "cubic-bezier(0.2, 0, 0, 1)",
};

/** Returns whether the draft height changed. */
function fitDraftHeight(
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

function clearDraftHeight(
  el: HTMLTextAreaElement,
  scrollHost: HTMLElement | undefined,
): boolean {
  const previous = el.style.height;
  el.style.height = "auto";
  el.style.overflowY = "hidden";
  if (scrollHost) syncThemedScrollbar(scrollHost);
  return previous !== "auto";
}

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

/** Unreachable hosts leave a rejected file chip. */
function rejectChipFor(name: string): ComposerPendingAttachment {
  return {
    id: crypto.randomUUID(),
    kind: "reject",
    name,
    mime: "application/octet-stream",
    byteLength: 0,
    rejectMessage: rejectMessageForCode("unsupported_attachment"),
  };
}

function chipTitle(att: ComposerPendingAttachment): string {
  if (att.kind === "reject") return att.rejectMessage || att.name;
  if (att.kind === "path-file" || att.kind === "path-folder") {
    return `${attachmentChipLabel(att)} · ${att.path}`;
  }
  if (att.kind === "artifact") return att.name;
  if (att.kind === "search-hit") return `${att.name} · ${att.sourceRef}`;
  if (att.kind === "secret") return `${att.name} · ${attachmentChipDetail(att)}`;
  return `${att.name} · ${formatAttachmentBytes(att.byteLength)} · ${attachmentPreviewSnippet(att)}`;
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
    !appBootPreparation.ready() || isComposerSendBlocked(
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
  const [dismissedSlashText, setDismissedSlashText] = createSignal<string | null>(null);
  const [activeSlashSuggestion, setActiveSlashSuggestion] = createSignal(0);
  const slashListboxId = `den-composer-slash-${++nextSlashListboxId}`;
  const attachments = () => pendingAttachmentsForSession(props.sessionId);
  const [composerErrors, setComposerErrors] = createSignal<Record<string, string>>({});
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
    removeMarkedDraftRange: (start, end, nextDraft) => removeMarkedDraftRange(start, end, nextDraft),
    growDraft: () => growDraft(),
  });
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
  const pastedTextAttachments = createPastedTextAttachmentController({
    destination,
    clearError: (target) => setComposerError(target, null),
  });
  const visiblePastedTextUploads = pastedTextAttachments.visibleUploads;
  const pasteAnnouncement = pastedTextAttachments.announcement;
  let inputRef: HTMLTextAreaElement | undefined;
  const elevated = createComposerElevatedAccess({
    client: () => { props.sidecarStatus; return getLycaonClient(); },
    connected: () => props.sidecarStatus === "connected",
    sessionId: () => props.sessionId,
    revision: () => `${props.projectId}:${props.approvalsRevision ?? 0}:${props.sidecarStatus}`,
    focus: () => inputRef?.focus({ preventScroll: true }),
  });
  const vaultUnlock = createComposerVaultUnlock({
    client: () => { props.sidecarStatus; return getLycaonClient(); },
    connected: () => props.sidecarStatus === "connected",
    sessionId: () => props.sessionId,
    revision: () => `${props.projectId}:${props.sidecarStatus}`,
  });
  let footerRef: HTMLElement | undefined;
  let bodyRef: HTMLDivElement | undefined;
  let scrollRef: HTMLDivElement | undefined;
  let composerHeight: HeightFollow | undefined;
  let fileInputRef: HTMLInputElement | undefined;
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
  let resolvedDocumentKey = "";
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
  const sendableAttachments = () => attachments().filter(isSendablePendingAttachment);
  const hasRejectChips = () =>
    attachments().some((a) => a.kind === "reject") ||
    visiblePastedTextUploads().some((upload) => upload.status === "failed");
  const hasPastedTextUploads = () => visiblePastedTextUploads().length > 0;
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

  const replaceText = (value: string) => {
    setText(value);
    // Unchanged values preserve the live selection.
    if (inputRef && inputRef.value !== value) inputRef.value = value;
  };

  const sizeDraft = (
    size: typeof fitDraftHeight,
    motion: "ease" | "snap",
  ) => {
    const el = inputRef;
    if (!el) return;
    const apply = () => size(el, scrollRef);
    if (!composerHeight) apply();
    else if (motion === "snap") composerHeight.snap(apply);
    else composerHeight.change(DRAFT_RESIZE_TIMING, apply);
  };
  /** Drafts scroll once the composer reaches its height cap. */
  const growDraft = () => sizeDraft(fitDraftHeight, "ease");
  const clearDraft = () => sizeDraft(clearDraftHeight, "snap");
  /** Conversation switches resize the draft without animation. */
  const snapDraft = () => sizeDraft(fitDraftHeight, "snap");

  const resizeChrome = (mutate: () => void) => {
    if (composerHeight) composerHeight.change(HEIGHT_TOGGLE_TIMING, mutate);
    else mutate();
  };

  onMount(() => {
    if (!footerRef || !bodyRef) return;
    const follow = followBodyHeight(footerRef, bodyRef);
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
    const el = inputRef;
    if (el && typeof document.execCommand === "function") {
      el.focus();
      el.setSelectionRange(start, end);
      if (document.execCommand("delete") && el.value === nextDraft) {
        setText(nextDraft);
        return;
      }
    }
    replaceText(nextDraft);
  };

  const writeDraft = (value: string): boolean => {
    const target = destination();
    if (!inlineTextWithinLimit(value, composerAttachmentCapabilities())) {
      setComposerError(target, INLINE_TEXT_TOO_LARGE_MESSAGE);
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
    if (composeBlocked()) return false;
    if (text().length > 0 && !inRecall(props.sessionId)) return false;
    if (
      inputRef &&
      !caretAtRecallEdge(
        inputRef.value,
        inputRef.selectionStart,
        inputRef.selectionEnd,
        direction,
      )
    ) {
      return false;
    }
    const recalled = walkRecall(props.sessionId, direction);
    if (recalled === null) return false;
    writeDraft(recalled);
    queueMicrotask(() => {
      if (!inputRef) return;
      growDraft();
      inputRef.setSelectionRange(inputRef.value.length, inputRef.value.length);
    });
    return true;
  };

  const slashSuggestions = createMemo(() =>
    filterWorkflowSlashSuggestions(text(), props.workflowCatalog ?? []),
  );
  const showSlashSuggestions = () =>
    !composeBlocked() &&
    text().trimStart().startsWith("/") &&
    slashSuggestions().length > 0 &&
    dismissedSlashText() !== text();

  createEffect(() => {
    const count = slashSuggestions().length;
    if (!showSlashSuggestions() || count === 0) {
      setActiveSlashSuggestion(0);
      return;
    }
    setActiveSlashSuggestion((index) => Math.min(index, count - 1));
  });

  const imageCount = () => attachments().filter((a) => countsAsImage(a.kind)).length;
  const byteAttachmentCount = () =>
    attachments().filter((a) => isByteAttachmentKind(a.kind)).length;
  const hasByteAttachments = () => attachments().some(isPayloadAttachment);

  // Unknown vision support remains distinct from unsupported.
  const visionHint = () => {
    if (imageCount() === 0 || props.visionSupport !== "unsupported") return null;
    const media = attachments().some((a) => a.kind === "video") ? "images or video frames" : "images";
    return `The active model cannot see ${media}. They will still appear in the transcript.`;
  };

  const applySlashSuggestion = (trigger: string) => {
    writeDraft(`${trigger} `);
    setDismissedSlashText(text());
    queueMicrotask(() => {
      growDraft();
      focusComposerInput(inputRef);
    });
  };

  const onSlashSuggestionKeyDown = (event: KeyboardEvent) => {
    if (
      !showSlashSuggestions() ||
      event.isComposing ||
      event.metaKey ||
      event.ctrlKey ||
      event.altKey ||
      event.shiftKey
    ) {
      return;
    }
    const suggestions = slashSuggestions();
    if (suggestions.length === 0) return;
    let handled = true;
    switch (event.key) {
      case "ArrowDown":
        setActiveSlashSuggestion((index) => (index + 1) % suggestions.length);
        break;
      case "ArrowUp":
        setActiveSlashSuggestion(
          (index) => (index - 1 + suggestions.length) % suggestions.length,
        );
        break;
      case "Home":
        setActiveSlashSuggestion(0);
        break;
      case "End":
        setActiveSlashSuggestion(suggestions.length - 1);
        break;
      case "Enter":
        {
          const suggestion = suggestions[activeSlashSuggestion()];
          if (suggestion) applySlashSuggestion(suggestion.trigger);
        }
        break;
      case "Escape":
        setDismissedSlashText(text());
        break;
      default:
        handled = false;
    }
    if (!handled) return;
    event.preventDefault();
    event.stopPropagation();
  };

  const clearAttachments = () => {
    const target = destination();
    pastedTextAttachments.cancelVisible();
    setComposerError(target, null);
    void clearComposerDocumentAttachments(target);
  };

  const interceptTextPaste = (pasted: string, start: number, end: number): boolean => {
    if (!pasted) return false;
    const shouldAttach = shouldAttachTextPaste({
      current: inputRef?.value ?? text(),
      selectionStart: start,
      selectionEnd: end,
      pasted,
      capabilities: composerAttachmentCapabilities(),
    });
    if (!shouldAttach) return false;
    exitRecall(props.sessionId);
    pastedTextAttachments.stage(pasted);
    return true;
  };

  const addFiles = async (files: File[]) => {
    if (files.length === 0) return;
    const target = destination();
    setComposerError(target, null);
    const caps = composerAttachmentCapabilities();
    if (!caps) {
      setComposerError(target, "Attachments are unavailable until host readiness loads.");
      return;
    }
    const activePastedUploads = visiblePastedTextUploads().filter(
      (upload) => upload.status === "uploading",
    );
    let availableCount = Math.max(
      0,
      caps.max_attachments - byteAttachmentCount() - activePastedUploads.length,
    );
    let availableBytes = Math.max(
      0,
        caps.max_turn_bytes -
        attachmentTurnBytes(attachments()) -
        activePastedUploads.reduce((sum, upload) => sum + upload.file.size, 0),
    );
    let availableImages = Math.max(0, caps.max_images - imageCount());
    const acceptedFiles: File[] = [];
    for (const file of files) {
      const looksLikeVideo = isAllowedVideoMime(file.type);
      // A video reaches the model as one frame sheet, so it spends an image slot too.
      const spendsImage = looksLikeVideo || isAllowedImageMime(file.type);
      if (
        availableCount <= 0 ||
        file.size > availableBytes ||
        (looksLikeVideo && file.size > caps.max_video_bytes) ||
        (spendsImage && availableImages <= 0)
      ) {
        continue;
      }
      acceptedFiles.push(file);
      availableCount -= 1;
      availableBytes -= file.size;
      if (spendsImage) availableImages -= 1;
    }
    if (acceptedFiles.length === 0) {
      setComposerError(target, rejectMessageForCode("attachment_too_large"));
      return;
    }
    if (acceptedFiles.length < files.length) {
      setComposerError(target, rejectMessageForCode("attachment_too_large"));
    }
    const pending: ComposerPendingAttachment[] = [];
    for (const file of acceptedFiles) {
      const client = getLycaonClient();
      const attachment = client
        ? await fileToComposerAttachment(file, target.projectId, client)
        : rejectChipFor(file.name);
      pending.push(attachment);
      if (attachment.kind === "reject" && attachment.rejectMessage) {
        setComposerError(target, attachment.rejectMessage);
      }
    }
    const result = await stageComposerMutation(target, pending);
    if (!result.ok) setComposerError(target, result.reason);
    // File-picker blur drops the composer focus claim.
    scheduleComposerFocus(inputRef);
  };

  const removeAttachment = (id: string) => {
    const target = destination();
    setComposerError(target, null);
    void removeComposerDocumentAttachment(target, id);
  };

  // Peer views share one host-managed chat document.
  createEffect(() => {
    const target = destination();
    const targetKey = `${target.projectId}\0${target.sessionId}`;
    if (targetKey === resolvedDocumentKey) return;
    resolvedDocumentKey = targetKey;
    void ensureComposerDocument(target);
  });

  // Mirror explicit shared-document updates into the mounted field.
  createEffect(() => {
    const stored = composerDraftForSession(props.sessionId);
    if (stored === text()) return;
    replaceText(stored);
    queueMicrotask(growDraft);
  });

  const insertNewline = () => {
    if (!inputRef || composeBlocked()) return;
    const el = inputRef;
    const start = el.selectionStart;
    const end = el.selectionEnd;
    const next = `${el.value.slice(0, start)}\n${el.value.slice(end)}`;
    if (!writeDraft(next)) return;
    queueMicrotask(() => {
      el.selectionStart = el.selectionEnd = start + 1;
      growDraft();
    });
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
    ) return;
    if (hasRejectChips()) {
      setComposerError(submittedDestination, "Remove unsupported attachments before sending");
      return;
    }
    if (!inlineTextWithinLimit(value, composerAttachmentCapabilities())) {
      setComposerError(
        submittedDestination,
        INLINE_TEXT_TOO_LARGE_MESSAGE,
      );
      return;
    }
    const pendingBytes = attachmentTurnBytes(pending);
    // Restored chips defer unknown limits to host validation at send time.
    const capabilities = composerAttachmentCapabilities();
    if (pendingBytes > 0 && capabilities &&
      (capabilities.max_turn_bytes <= 0 || pendingBytes > capabilities.max_turn_bytes)) {
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
    onCleanup(registerCommandHandler("composer.send", () => {
      void submit();
    }));
  });
  createEffect(() => {
    if (!inputFocused()) return;
    onCleanup(registerCommandHandler("composer.newline", () => {
      insertNewline();
    }));
    onCleanup(registerCommandHandler(
      "composer.recallPrev",
      declinable(() => walkPromptRecall("older")),
    ));
    onCleanup(registerCommandHandler(
      "composer.recallNext",
      declinable(() => walkPromptRecall("newer")),
    ));
  });
  const detachListener = attachDispatcher();
  onCleanup(() => {
    detachListener();
    setComposerFocused(false, focusClaim);
    setAskSendArmed(false, focusClaim);
    releaseFocusRegion("composer", focusClaim);
  });

  createEffect(
    on(
      () => props.sessionId,
      (sessionId, prevSessionId) => {
        if (prevSessionId) void flushComposerDraftsToDisk();
        replaceText(composerDraftForSession(sessionId));
        queueMicrotask(snapDraft);
      },
      { defer: true },
    ),
  );

  createEffect(
    on(
      () => props.focusWhen,
      (focusWhen) => {
        if (!focusWhen || composeBlocked()) return;
        scheduleComposerFocus(inputRef);
      },
    ),
  );

  createEffect(
    on(composeBlocked, (isDisabled, wasDisabled) => {
      if (isDisabled || wasDisabled !== false || !props.focusWhen) return;
      scheduleComposerFocus(inputRef);
    }),
  );

  createEffect(
    on(
      () => props.refocusPulse,
      () => {
        if (composeBlocked()) return;
        scheduleComposerFocus(inputRef);
      },
      { defer: true },
    ),
  );

  createEffect(
    on(
      () => props.rehydrateDraftPulse,
      () => {
        replaceText(composerDraftForSession(props.sessionId));
        queueMicrotask(() => {
          if (!inputRef) return;
          growDraft();
          // Explicit edit preloads place the caret after the draft.
          inputRef.setSelectionRange(inputRef.value.length, inputRef.value.length);
        });
      },
      { defer: true },
    ),
  );

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
            <strong>{APPROVALS_COPY.card.composerRedirect.title}</strong>{" — "}
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
        <Show when={attachments().length > 0 || visiblePastedTextUploads().length > 0}>
          <div class="den-composer-attach-note">
            <span data-testid="composer-attach-count">
              {visiblePastedTextUploads().some((upload) => upload.status === "uploading")
                ? `${visiblePastedTextUploads().filter((upload) => upload.status === "uploading").length} attaching`
                : visiblePastedTextUploads().some((upload) => upload.status === "failed")
                  ? `${visiblePastedTextUploads().filter((upload) => upload.status === "failed").length} needs attention`
                  : attachments().length === 1
                  ? "1 attached"
                  : `${attachments().length} attached`}
            </span>
            <Show when={hasByteAttachments() || visiblePastedTextUploads().length > 0}>
              <span class="den-composer-attach-note-hint" data-testid="composer-provider-send-hint">
                {ATTACHMENT_PROVIDER_SEND_HINT}
              </span>
            </Show>
            <button
              type="button"
              class="den-composer-attach-clear"
              data-testid="composer-attach-clear"
              onClick={clearAttachments}
            >
              Clear all
            </button>
          </div>
          <Scrollport
            class="den-composer-chip-rail"
            contentAs="ul"
            contentClass="den-composer-chip-list"
            content={{ "data-testid": "composer-attachment-chips" }}
          >
            <For each={visiblePastedTextUploads()}>
              {(upload) => (
                <li
                  class="den-composer-chip"
                  classList={{ "den-composer-chip--reject": upload.status === "failed" }}
                  data-testid="composer-pasted-text-upload"
                  data-kind="text"
                  aria-busy={upload.status === "uploading" ? "true" : undefined}
                  data-tip={`${upload.file.name} · ${formatAttachmentBytes(upload.file.size)}`}
                  data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
                >
                  <span class="den-composer-chip-glyph" aria-hidden="true">≡</span>
                  <span class="den-composer-chip-label">{upload.file.name}</span>
                  <span class="den-composer-chip-detail">
                    {upload.status === "uploading"
                      ? `${formatAttachmentBytes(upload.file.size)} · Attaching…`
                      : upload.error}
                  </span>
                  <Show when={upload.status === "failed"}>
                    <button
                      type="button"
                      class="den-composer-chip-remove"
                      data-testid="composer-pasted-text-retry"
                      aria-label="Retry pasted text attachment"
                      onClick={() => void pastedTextAttachments.retry(upload)}
                    >
                      ↻
                    </button>
                  </Show>
                  <button
                    type="button"
                    class="den-composer-chip-remove"
                    data-testid="composer-pasted-text-remove"
                    aria-label="Remove pasted text attachment"
                    onClick={() => pastedTextAttachments.remove(upload.id)}
                  >
                    ×
                  </button>
                </li>
              )}
            </For>
            <For each={attachments()}>
              {(att) => (
                <li
                  class="den-composer-chip"
                  classList={{ "den-composer-chip--reject": att.kind === "reject" }}
                  data-testid="composer-attachment-chip"
                  data-kind={att.kind}
                  data-reject-code={att.kind === "reject" ? (att.rejectCode ?? "") : ""}
                  data-tip={chipTitle(att)}
                  data-tip-when-clipped=".den-composer-chip-label, .den-composer-chip-detail"
                >
                  <Show
                    when={
                      (att.kind === "image" || att.kind === "artifact") && att.previewUrl
                        ? att.previewUrl
                        : undefined
                    }
                    keyed
                    fallback={
                      <span class="den-composer-chip-glyph" aria-hidden="true">
                        {attachmentKindGlyph(att.kind)}
                      </span>
                    }
                  >
                    {(url) => (
                      <img
                        class="den-composer-chip-thumb"
                        src={url}
                        alt={
                          att.kind === "image"
                            ? `${att.name} (${formatAttachmentBytes(att.byteLength)})`
                            : att.name
                        }
                      />
                    )}
                  </Show>
                  <span class="den-composer-chip-label">{attachmentChipLabel(att)}</span>
                  <Show when={attachmentChipDetail(att)}>
                    <span class="den-composer-chip-detail" data-testid="composer-attach-preview">
                      {attachmentChipDetail(att)}
                    </span>
                  </Show>
                  <button
                    type="button"
                    class="den-composer-chip-remove"
                    data-testid="composer-image-remove"
                    aria-label={`Remove ${attachmentChipLabel(att)}`}
                    onClick={() => removeAttachment(att.id)}
                  >
                    ×
                  </button>
                </li>
              )}
            </For>
          </Scrollport>
        </Show>
        <Show when={showSlashSuggestions()}>
          <AnchoredSurface
            id={slashListboxId}
            class="den-composer-slash-suggestions"
            testId="composer-slash-suggestions"
            role="listbox"
            ariaLabel="Workflow slash commands"
            anchor={() => footerRef}
            preferredSide="top"
            width="anchor"
            gap={6}
            onDismiss={() => setDismissedSlashText(text())}
          >
            <For each={slashSuggestions()}>
              {(row, index) => (
                <button
                  id={`${slashListboxId}-option-${index()}`}
                  type="button"
                  role="option"
                  tabindex={-1}
                  aria-selected={index() === activeSlashSuggestion()}
                  class="den-composer-slash-suggestion"
                  classList={{
                    "den-composer-slash-suggestion--active":
                      index() === activeSlashSuggestion(),
                  }}
                  data-testid={`composer-slash-${row.trigger.slice(1)}`}
                  onMouseEnter={() => setActiveSlashSuggestion(index())}
                  onClick={() => applySlashSuggestion(row.trigger)}
                >
                  <span class="den-composer-slash-suggestion-trigger">{row.trigger}</span>
                  <span class="den-composer-slash-suggestion-label">{row.label}</span>
                </button>
              )}
            </For>
          </AnchoredSurface>
        </Show>
        <div class="den-composer-row">
          <Scrollport ref={(el) => { scrollRef = el; }} class="den-composer-scroll">
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
                const files = filesFromClipboard(e.clipboardData?.items);
                if (files.length > 0) {
                  e.preventDefault();
                  void addFiles(files);
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
              disabled={
                composeBlocked() ||
                !composerAttachmentCapabilities() ||
                byteAttachmentCount() +
                  visiblePastedTextUploads().filter((upload) => upload.status === "uploading")
                    .length >=
                  (composerAttachmentCapabilities()?.max_attachments ?? 0)
              }
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
