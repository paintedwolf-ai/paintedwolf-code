import { createStore, reconcile } from "solid-js/store";
import type { Session, SessionMode } from "../../api/types.ts";
import { chatPrefsReady, lastStartedModePref, rememberStartedMode } from "../../settings/chat/chat-prefs.ts";
import { peekArmWorkflow } from "../workflow/pending-arm.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { applyCachedSessionMode } from "./session-chat-cache.ts";

type UpgradePhase = "sending" | "reconciling";
type ModeIntent = {
  initial?: SessionMode;
  draft?: SessionMode;
  admission?: { mode: SessionMode; count: number; remember: () => void };
  upgrade?: UpgradePhase;
};

function createModeIntents() {
  return createStore<Record<string, ModeIntent | undefined>>({});
}

const intentsByStore = new WeakMap<object, ReturnType<typeof createModeIntents>>();

function modeIntents(context: object) {
  let intents = intentsByStore.get(context);
  if (!intents) {
    intents = createModeIntents();
    intentsByStore.set(context, intents);
  }
  return intents;
}

function rootSessionFor(appStore: AppStore, sessionId: string): Session | undefined {
  const session = appStore.state.currentSession;
  return session?.id === sessionId && !session.parent_session_id ? session : undefined;
}

function defaultModeFor(sessionId: string): SessionMode {
  return peekArmWorkflow(sessionId)?.requires_agent ? "agent" : lastStartedModePref();
}

export function captureInitialSessionMode(appStore: AppStore, sessionId: string): void {
  const session = rootSessionFor(appStore, sessionId);
  const [intents, setIntent] = modeIntents(appStore.actions);
  const entry = intents[sessionId];
  if (!chatPrefsReady() || !session || session.started || entry?.initial) return;
  setIntent(sessionId, { ...entry, initial: defaultModeFor(sessionId) });
}

export function sessionModeView(appStore: AppStore, sessionId: string) {
  const session = rootSessionFor(appStore, sessionId);
  const [intents] = modeIntents(appStore.actions);
  const intent = intents[sessionId];
  const started = session?.started === true;
  const admission = intent?.admission;
  const confirmed = session?.mode;
  const upgrade = started && confirmed === "ask" ? intent?.upgrade : undefined;
  const mode = !session || started
    ? confirmed
    : admission?.mode ?? intent?.draft ?? intent?.initial ?? (chatPrefsReady() ? defaultModeFor(sessionId) : undefined);
  return {
    mode,
    started,
    admissionPending: admission !== undefined,
    upgrade,
    show: session !== undefined && mode !== undefined &&
      (mode === "ask" || !started),
    workflowNeedsAgent: mode === "ask" && !upgrade,
  };
}

export function setDraftSessionMode(appStore: AppStore, sessionId: string, mode: SessionMode): void {
  const [intents, setIntent] = modeIntents(appStore.actions);
  const view = sessionModeView(appStore, sessionId);
  if (!view.show || view.started || view.admissionPending) return;
  setIntent(sessionId, { ...intents[sessionId], draft: mode });
}

export function beginSessionModeAdmission(appStore: AppStore, sessionId: string, explicit?: SessionMode) {
  const [intents, setIntent] = modeIntents(appStore.actions);
  const session = rootSessionFor(appStore, sessionId);
  const entry = intents[sessionId];
  if (entry?.upgrade) return {
    mode: "agent" as const,
    confirm: () => confirmSessionModeAdmission(appStore, sessionId, "agent"),
    finish() {},
  };
  const mode = entry?.admission?.mode ?? explicit ?? sessionModeView(appStore, sessionId).mode ?? session?.mode;
  if (!session || session.started || mode === undefined) return {
    mode: explicit,
    confirm: () => confirmSessionModeAdmission(appStore, sessionId, explicit),
    finish() {},
  };
  let remembered = false;
  const remember = entry?.admission?.remember ?? (() => {
    if (remembered) return;
    remembered = true;
    rememberStartedMode(mode);
  });
  setIntent(sessionId, {
    ...entry,
    initial: entry?.initial ?? mode,
    admission: { mode, count: (entry?.admission?.count ?? 0) + 1, remember },
  });
  return {
    mode,
    confirm() {
      remember();
      confirmSessionModeAdmission(appStore, sessionId, mode);
    },
    finish() {
      const current = intents[sessionId];
      if (current?.admission?.remember !== remember) return;
      const { count } = current.admission;
      setIntent(sessionId, {
        ...current,
        admission: count > 1 ? { mode, count: count - 1, remember } : undefined,
      });
    },
  };
}

export function setSessionModeUpgrade(appStore: AppStore, sessionId: string, upgrade: UpgradePhase | undefined) {
  const [intents, setIntent] = modeIntents(appStore.actions);
  setIntent(sessionId, { ...intents[sessionId], upgrade });
}

export function settleSessionModeIntent(context: object, session: { id: string; mode?: SessionMode; started?: boolean }) {
  const [intents, setIntent] = modeIntents(context);
  const current = intents[session.id];
  if (!session.started || !current) return;
  setIntent(session.id, session.mode === "agent"
    ? undefined
    : { upgrade: current.upgrade });
}

export function rebaseSessionModeIntents(context: object): void {
  const current = intentsByStore.get(context);
  if (!current) return;
  const [intents, setIntent] = current;
  const retained: Record<string, ModeIntent> = {};
  for (const [sessionId, intent] of Object.entries(intents)) {
    if (intent?.initial || intent?.draft || intent?.upgrade) retained[sessionId] = {
      initial: intent.initial,
      draft: intent.draft,
      upgrade: intent.upgrade ? "reconciling" : undefined,
    };
  }
  setIntent(reconcile(retained));
}

export function confirmSessionModeAdmission(appStore: AppStore, sessionId: string, mode: SessionMode | undefined) {
  if (!mode) return;
  const projection = { mode, started: true };
  applyCachedSessionMode(sessionId, projection);
  appStore.actions.applySessionMode(sessionId, projection);
}
