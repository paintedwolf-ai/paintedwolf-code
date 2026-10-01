import { createSignal } from "solid-js";
import { isNativeUIHandlerId } from "../contributions/native-ui-handlers.generated.ts";
import {
  contributionCommand,
  contributionCommandAvailable,
} from "../contributions/dispatch.ts";
import {
  bindingActiveNow,
  bindingAllowsInput,
  liveResolvedKeymap,
} from "../contributions/frame-keymap.ts";
import {
  chordBypassesTextInput,
  eventToBareKey,
  eventToChord,
  type KeyboardEventLike,
} from "./chord.ts";
import {
  commandsForChord,
  commandsForSequence,
  leaderIsPrefix,
  type CommandScope,
  type ResolvedKeymap,
} from "./keymap.ts";
import { shortcutPlatform } from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";
import { createClaimable } from "../platform/interaction/claimable.ts";

/** Signals that a handler left its key unconsumed. */
export const COMMAND_DECLINED = "command-declined" as const;

export type CommandHandler = () => unknown;

/** Maps a boolean action result to shortcut consumption. */
export function declinable(handler: () => boolean): CommandHandler {
  return () => (handler() ? undefined : COMMAND_DECLINED);
}

/** Invokes a host-executed contribution command. */
export type HostCommandInvoker = (commandId: string) => void;

export type DispatcherContext = {
  /** Overlay / modal claims list-navigation chords. */
  overlayActive: boolean;
  /** Message composer textarea focused. */
  composerFocused: boolean;
  /** Files stage is the shell foreground. */
  filesStageActive: boolean;
};

const PENDING_LEADER_TIMEOUT_MS = 1500;

type PendingLeaderState = {
  leaderChord: string;
  expiryTimer: ReturnType<typeof setTimeout>;
};

/** Registration stack per handler id; the last entry is active. */
const handlers = new Map<string, CommandHandler[]>();
/** The shell's invoke lane for commands with no native handler. */
let hostInvoker: HostCommandInvoker | null = null;
let overrides: Record<string, string> | null = null;
const overlayScopeClaims = new Set<object>();
let filesStageActive = false;
/** Composer focus belongs to one mounted composer. */
let composerFocus = createClaimable<true>();
/** Tracking handle for surfaces that publish composer focus as a fact. */
const [composerFocusVersion, bumpComposerFocus] = createSignal(0);
/** Lets the selected ask control submit with Enter. */
let askSendArm = createClaimable<true>();
/** Active modal command boundaries. */
const shortcutBoundaryClaims = new Set<object>();
let captureActive = false;
/** Optional context override for tests. */
let contextProvider: (() => DispatcherContext) | null = null;
let platformOverride: TauriPlatform | null = null;
/** Active shared-listener holders. */
let attachCount = 0;
let pendingLeader: PendingLeaderState | null = null;

function clearPendingLeader(): void {
  if (!pendingLeader) return;
  clearTimeout(pendingLeader.expiryTimer);
  pendingLeader = null;
}

function startPendingLeader(leaderChord: string): void {
  clearPendingLeader();
  pendingLeader = {
    leaderChord,
    expiryTimer: setTimeout(clearPendingLeader, PENDING_LEADER_TIMEOUT_MS),
  };
}

/** The armed leader chord, or null when no sequence is pending. */
export function pendingLeaderChord(): string | null {
  return pendingLeader?.leaderChord ?? null;
}

/** Suspends commands during shortcut capture. */
export function setShortcutCaptureActive(active: boolean): void {
  captureActive = active;
  if (active) clearPendingLeader();
}

/** Stacks handlers by mount order. */
export function registerCommandHandler(
  id: string,
  handler: CommandHandler,
): () => void {
  if (!isNativeUIHandlerId(id)) {
    throw new Error(`unknown native handler id ${id}`);
  }
  const stack = handlers.get(id) ?? [];
  handlers.set(id, [...stack, handler]);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    const current = handlers.get(id);
    if (!current) return;
    const next = current.filter((entry) => entry !== handler);
    if (next.length > 0) handlers.set(id, next);
    else handlers.delete(id);
  };
}

/** Installs the host-command invoke lane. */
export function registerHostCommandInvoker(
  invoker: HostCommandInvoker,
): () => void {
  hostInvoker = invoker;
  return () => {
    if (hostInvoker === invoker) hostInvoker = null;
  };
}

/** Context menus enter the same form/result lane as keyboard activation. */
export function invokeHostCommand(commandId: string): void {
  hostInvoker?.(commandId);
}

/** The active implementation for a handler id. */
function currentHandler(id: string): CommandHandler | undefined {
  const stack = handlers.get(id);
  return stack && stack.length > 0 ? stack[stack.length - 1] : undefined;
}

/** How a handler-less or handler-run invocation resolved. */
export type InvokeCommandOutcome = "ran" | "declined" | "missing";

/** Runs a registered handler without shortcut gates, honoring declines. */
export function invokeCommand(id: string): InvokeCommandOutcome {
  const handler = currentHandler(id);
  if (!handler) return "missing";
  return handler() === COMMAND_DECLINED ? "declined" : "ran";
}

export function setShortcutOverrides(
  next: Record<string, string> | null | undefined,
): void {
  overrides = next ?? null;
}

/** The native handler a frame command names, when it has one. */
function handlerFor(commandId: string): CommandHandler | undefined {
  const handlerId = contributionCommand(commandId)?.handler_id;
  return handlerId ? currentHandler(handlerId) : undefined;
}

function addClaim(claims: Set<object>): () => void {
  const claim = {};
  claims.add(claim);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    claims.delete(claim);
  };
}

/** Claims overlay-scoped keyboard commands. */
export function claimOverlayScope(): () => void {
  clearPendingLeader();
  return addClaim(overlayScopeClaims);
}

/** Suspends app commands behind one modal. */
export function claimShortcutBoundary(): () => void {
  clearPendingLeader();
  return addClaim(shortcutBoundaryClaims);
}

export function setFilesStageActive(active: boolean): void {
  filesStageActive = active;
}

/** Updates focus for one composer instance. */
export function setComposerFocused(focused: boolean, claimToken: object): void {
  if (focused) composerFocus.claim(true, claimToken);
  else composerFocus.release(claimToken);
  bumpComposerFocus((n) => n + 1);
}

/** Returns reactive composer focus state. */
export function isComposerFocused(): boolean {
  composerFocusVersion();
  return composerFocus.get() !== null;
}

/** Updates the ask-submit claim for one composer instance. */
export function setAskSendArmed(armed: boolean, claimToken: object): void {
  if (armed) askSendArm.claim(true, claimToken);
  else askSendArm.release(claimToken);
}

/** Overrides live scope state in tests. */
export function setDispatcherContextForTests(
  provider: () => DispatcherContext,
): void {
  contextProvider = provider;
}

function currentContext(): DispatcherContext {
  if (contextProvider) return contextProvider();
  return {
    overlayActive: overlayScopeClaims.size > 0,
    composerFocused: composerFocus.get() !== null,
    filesStageActive,
  };
}

/** Overrides the shortcut platform in tests. */
export function setDispatcherPlatformForTests(
  platform: TauriPlatform | null,
): void {
  platformOverride = platform;
}

export function resetDispatcherForTests(): void {
  handlers.clear();
  hostInvoker = null;
  overrides = null;
  overlayScopeClaims.clear();
  filesStageActive = false;
  // Test resets replace claimable state.
  composerFocus = createClaimable();
  askSendArm = createClaimable();
  shortcutBoundaryClaims.clear();
  captureActive = false;
  contextProvider = null;
  platformOverride = null;
  clearPendingLeader();
  unbindKeydown();
}

function isAskChoiceControl(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLInputElement) || target.disabled) return false;
  return target.type === "radio" || target.type === "checkbox";
}

function activePlatform(): TauriPlatform {
  return platformOverride ?? shortcutPlatform();
}

function resolved(): ResolvedKeymap {
  return liveResolvedKeymap(activePlatform(), overrides);
}

/** A control whose bare Enter/Space keydown becomes a native click. */
function isNativeActivationTarget(target: EventTarget | null): boolean {
  if (target == null || typeof target !== "object") return false;
  const el = target as HTMLElement;
  return typeof el.tagName === "string" && el.tagName === "BUTTON";
}

export function isEditableTarget(target: EventTarget | null): boolean {
  if (target == null || typeof target !== "object") return false;
  const el = target as HTMLElement;
  if (typeof el.tagName !== "string") return false;
  if (el.isContentEditable) return true;
  const ce =
    typeof el.getAttribute === "function"
      ? el.getAttribute("contenteditable")
      : null;
  if (ce != null && ce !== "false") return true;
  const tag = el.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/** Ask choice controls borrow whichever command runs the composer send. */
const COMPOSER_SEND_HANDLER = "composer.send";

const SCOPE_RANK: Record<CommandScope, number> = {
  overlay: 4,
  composer: 3,
  files: 2,
  global: 1,
};

function scopeAllowed(
  scope: CommandScope,
  ctx: DispatcherContext,
): boolean {
  if (scope === "overlay") return ctx.overlayActive;
  if (scope === "composer") return ctx.composerFocused;
  if (scope === "files") return ctx.filesStageActive;
  return true;
}

type ScopedBinding = {
  bindingId: string;
  commandId: string;
  scope: CommandScope;
};

/** Returns one highest-precedence binding, or null when the top stratum conflicts. */
function bestAllowed<T extends ScopedBinding>(
  candidates: readonly T[],
  ctx: DispatcherContext,
): T | null {
  let best: T | null = null;
  let ambiguous = false;
  for (const c of candidates) {
    if (!scopeAllowed(c.scope, ctx)) continue;
    if (!contributionCommandAvailable(c.commandId)) continue;
    if (!bindingActiveNow(c.bindingId)) continue;
    if (!best) {
      best = c;
      continue;
    }
    const rank = SCOPE_RANK[c.scope] ?? 0;
    const bestRank = SCOPE_RANK[best.scope] ?? 0;
    if (rank > bestRank) {
      best = c;
      ambiguous = false;
    } else if (rank === bestRank && c.bindingId !== best.bindingId) {
      ambiguous = true;
    }
  }
  return ambiguous ? null : best;
}

function matchBinding(
  chord: string,
  ctx: DispatcherContext,
  keymap: ResolvedKeymap = resolved(),
) {
  return bestAllowed(commandsForChord(keymap, chord), ctx);
}

function matchSequenceBinding(
  leader: string,
  secondKey: string,
  ctx: DispatcherContext,
  keymap: ResolvedKeymap = resolved(),
) {
  return bestAllowed(commandsForSequence(keymap, leader, secondKey), ctx);
}

function selectedBinding(
  binding: string,
  ctx: DispatcherContext,
  keymap: ResolvedKeymap = resolved(),
): ScopedBinding | null {
  const split = binding.indexOf(" ");
  return split < 0
    ? matchBinding(binding, ctx, keymap)
    : matchSequenceBinding(
        binding.slice(0, split),
        binding.slice(split + 1),
        ctx,
        keymap,
      );
}

function bindingAcceptsTarget(
  binding: ScopedBinding,
  chord: string,
  target: EventTarget | null | undefined,
): boolean {
  const editingInput = isEditableTarget(target ?? null);
  if (editingInput && binding.scope === "files" && contributionCommand(binding.commandId)?.category === "editing") {
    const element = target as HTMLElement;
    if (typeof element.closest !== "function" || !element.closest(".cm-content")) return false;
  }
  return (
    !editingInput ||
    bindingAllowsInput(binding.bindingId) ||
    chordBypassesTextInput(chord, activePlatform())
  );
}

function runBinding(binding: ScopedBinding): boolean {
  const handler = handlerFor(binding.commandId);
  if (handler) return handler() !== COMMAND_DECLINED;
  if (hostInvoker && contributionCommand(binding.commandId)?.executor !== "den") {
    hostInvoker(binding.commandId);
    return true;
  }
  return false;
}

/** Menu and keyboard commands both stop at capture and modal boundaries. */
export function shortcutCommandsSuspended(): boolean {
  return captureActive || shortcutBoundaryClaims.size > 0;
}

/** Re-check the declaration behind a native menu accelerator at event time. */
export function shortcutBindingAvailable(
  bindingId: string,
  binding: string,
  target?: EventTarget | null,
): boolean {
  if (shortcutCommandsSuspended()) return false;
  if (pendingLeader && !binding.includes(" ")) return false;
  const selected = selectedBinding(binding, currentContext());
  if (selected?.bindingId !== bindingId) return false;
  const chord = binding.includes(" ") ? binding.slice(0, binding.indexOf(" ")) : binding;
  return bindingAcceptsTarget(selected, chord, target);
}

/** Runs one declaration through the shared shortcut gate. */
export function invokeShortcutBinding(
  bindingId: string,
  binding: string,
  target?: EventTarget | null,
): boolean {
  if (!shortcutBindingAvailable(bindingId, binding, target)) return false;
  const selected = selectedBinding(binding, currentContext());
  return selected ? runBinding(selected) : false;
}

/** Checks a local key handler against the shared shortcut gate. */
export function shortcutCommandMatchesEvent(
  commandId: string,
  event: KeyboardEventLike & {
    target?: EventTarget | null;
    isComposing?: boolean;
  },
): boolean {
  if (event.isComposing || shortcutCommandsSuspended() || pendingLeader) {
    return false;
  }
  const chord = eventToChord(event, activePlatform());
  if (!chord) return false;
  const selected = matchBinding(chord, currentContext());
  if (
    (chord === "Enter" || chord === "Space") &&
    isNativeActivationTarget(event.target ?? null)
  ) {
    return false;
  }
  return (
    selected?.commandId === commandId &&
    bindingAcceptsTarget(selected, chord, event.target)
  );
}

export type DispatchResult = {
  commandId: string | null;
  chord: string | null;
  handled: boolean;
};

function isModifierOnlyEvent(event: KeyboardEventLike): boolean {
  const key = event.key;
  return (
    key === "Control" ||
    key === "Shift" ||
    key === "Alt" ||
    key === "Meta" ||
    key === "OS"
  );
}

/** Resolves and invokes a keyboard command. */
export function dispatchKeyboardEvent(
  event: KeyboardEventLike & {
    target?: EventTarget | null;
    repeat?: boolean;
    isComposing?: boolean;
  },
): DispatchResult {
  if (captureActive) {
    return { commandId: null, chord: null, handled: false };
  }
  if (event.isComposing) {
    return { commandId: null, chord: null, handled: false };
  }
  if (shortcutBoundaryClaims.size > 0) {
    return { commandId: null, chord: null, handled: false };
  }

  const ctx = currentContext();
  const keymap = resolved();

  // Consume second keys before editable controls receive them.
  if (pendingLeader) {
    if (isModifierOnlyEvent(event)) {
      return { commandId: null, chord: pendingLeader.leaderChord, handled: false };
    }
    if (event.key === "Escape") {
      clearPendingLeader();
      return { commandId: null, chord: "Escape", handled: true };
    }

    const chord = eventToChord(event, activePlatform());
    if (chord && chord === pendingLeader.leaderChord) {
      // A repeated leader restarts the pending sequence.
      if (event.repeat) {
        return { commandId: null, chord, handled: true };
      }
      startPendingLeader(chord);
      return { commandId: null, chord, handled: true };
    }

    const bare = eventToBareKey(event);
    const leader = pendingLeader.leaderChord;
    clearPendingLeader();
    if (!bare) {
      // Invalid second keys still finish the sequence.
      return { commandId: null, chord: chord ?? null, handled: true };
    }
    const matched = matchSequenceBinding(leader, bare, ctx, keymap);
    if (!matched) {
      return { commandId: null, chord: `${leader} ${bare}`, handled: true };
    }
    const commandId = matched.commandId;
    // The leader controls its second key even when the action declines.
    runBinding(matched);
    return { commandId, chord: `${leader} ${bare}`, handled: true };
  }

  const chord = eventToChord(event, activePlatform());
  if (!chord) return { commandId: null, chord: null, handled: false };

  // A sequence starts only outside overlay scope.
  if (leaderIsPrefix(keymap, chord) && !ctx.overlayActive) {
    if (event.repeat) {
      return { commandId: null, chord, handled: true };
    }
    startPendingLeader(chord);
    return { commandId: null, chord, handled: true };
  }

  let matched = matchBinding(chord, ctx, keymap);
  let commandId = matched?.commandId ?? null;
  // Armed choice controls share the composer send command.
  if (!commandId && askSendArm.get() && !ctx.overlayActive) {
    const sendBound = bestAllowed(
      commandsForChord(keymap, chord).filter(
        (binding) =>
          contributionCommand(binding.commandId)?.handler_id ===
          COMPOSER_SEND_HANDLER,
      ),
      { ...ctx, composerFocused: true },
    );
    if (sendBound && isAskChoiceControl(event.target ?? null)) {
      matched = sendBound;
      commandId = sendBound.commandId;
    }
  }
  if (!commandId) return { commandId: null, chord, handled: false };

  if (!matched || !bindingAcceptsTarget(matched, chord, event.target)) {
    return { commandId: null, chord, handled: false };
  }
  // Native activation keys remain handled by buttons.
  if (
    (chord === "Enter" || chord === "Space") &&
    isNativeActivationTarget(event.target ?? null)
  ) {
    return { commandId: null, chord, handled: false };
  }

  return { commandId, chord, handled: runBinding(matched) };
}

function onWindowKeyDown(event: KeyboardEvent): void {
  if (captureActive) {
    event.preventDefault();
    event.stopPropagation();
    return;
  }
  // Closer handlers consume the event.
  if (event.defaultPrevented) return;
  const result = dispatchKeyboardEvent(event);
  if (result.handled) {
    event.preventDefault();
    event.stopPropagation();
  }
}

function onWindowBlur(): void {
  clearPendingLeader();
}

function unbindKeydown(): void {
  if (typeof window === "undefined") return;
  attachCount = 0;
  window.removeEventListener("keydown", onWindowKeyDown);
  window.removeEventListener("blur", onWindowBlur);
}

/** Claims the shared window shortcut listener. */
export function attachDispatcher(): () => void {
  if (typeof window === "undefined") return () => {};
  if (attachCount === 0) {
    window.addEventListener("keydown", onWindowKeyDown);
    window.addEventListener("blur", onWindowBlur);
  }
  attachCount += 1;
  let released = false;
  return () => {
    if (released) return;
    released = true;
    // Test resets may release the shared listener first.
    if (attachCount === 0) return;
    attachCount -= 1;
    if (attachCount === 0) {
      clearPendingLeader();
      unbindKeydown();
    }
  };
}
