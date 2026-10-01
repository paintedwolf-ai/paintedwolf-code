import { batch, createEffect, createSignal, untrack } from "solid-js";

export type ResidentPresence = "active" | "pending" | "idle";

export type ResidentStack = {
  keys: () => string[];
  presence: (key: string) => ResidentPresence;
  pending: () => string | null;
  displayed: () => string | null;
  generation: (key: string) => number;
  markReady: (key: string, generation?: number) => void;
  /** An idle surface whose presentation went stale prepares again before it returns. */
  withdraw: (key: string) => void;
};

export type ResidentStackOptions = {
  generation?: (key: string) => number;
  /** Retains keys accepted by the active scope. */
  retain?: (key: string) => boolean;
  /** Keeps a removed tab painted until its replacement is prepared. */
  retainOutgoing?: boolean;
  /** Additional preparation required by a sibling surface. */
  canPublish?: (key: string) => boolean;
};

/** Keeps resident surfaces mounted and gates new surfaces on readiness. */
export function useResidentStack(
  active: () => string | null,
  options?: ResidentStackOptions,
): ResidentStack {
  const [keys, setKeys] = createSignal<string[]>([]);
  const [shown, setShown] = createSignal<string | null>(null);
  const [pending, setPending] = createSignal<string | null>(null);
  const [preparationRevision, setPreparationRevision] = createSignal(0);
  const prepared = new Map<string, number>();
  const generation = (key: string) => {
    // Removed previews keep their published generation until handoff.
    const published = prepared.get(key);
    if (options?.retainOutgoing && options.retain?.(key) === false && published !== undefined) {
      return published;
    }
    return options?.generation?.(key) ?? 0;
  };
  const isPrepared = (key: string) => prepared.get(key) === generation(key);

  const presence = (key: string): ResidentPresence => {
    if (pending() === key) return "pending";
    if (shown() === key) return "active";
    return "idle";
  };

  const markReady = (key: string, payloadGeneration = generation(key)) => {
    if (!untrack(keys).includes(key) || payloadGeneration !== generation(key)) return;
    batch(() => {
      prepared.set(key, payloadGeneration);
      setPreparationRevision((value) => value + 1);
      if (untrack(pending) !== key || options?.canPublish?.(key) === false) return;
      setShown(key);
      setPending(null);
    });
  };

  const withdraw = (key: string) => {
    if (untrack(shown) === key || untrack(pending) === key || !prepared.has(key)) return;
    prepared.delete(key);
    setPreparationRevision((value) => value + 1);
  };

  createEffect(() => {
    void preparationRevision();
    const retain = options?.retain;
    const raw = active();
    const next = raw && (!retain || retain(raw)) ? raw : null;
    const prevKeys = untrack(keys);
    const previousShown = shown();
    const outgoing = options?.retainOutgoing && next && previousShown !== next &&
      previousShown && isPrepared(previousShown) ? previousShown : null;
    const kept = retain ? prevKeys.filter((key) => key === outgoing || retain(key)) : prevKeys;
    for (const key of prepared.keys()) if (!kept.includes(key)) prepared.delete(key);
    const wasKnown = Boolean(next && isPrepared(next));
    const canPublish = !next || options?.canPublish?.(next) !== false;
    const nextKeys =
      next && !kept.includes(next)
        ? [...kept, next]
        : kept;
    if (
      nextKeys.length !== prevKeys.length ||
      nextKeys.some((k, i) => k !== prevKeys[i])
    ) {
      setKeys(nextKeys);
    }
    if (previousShown && !nextKeys.includes(previousShown)) {
      setShown(null);
    }

    if (!next) {
      setPending(null);
      setShown(null);
      return;
    }

    const current = untrack(shown);
    const incoming = untrack(pending);
    if (current === next) {
      if (incoming) setPending(null);
      return;
    }
    if (incoming === next && (!wasKnown || !canPublish)) return;

    if (canPublish && (wasKnown || current == null)) {
      setShown(next);
      setPending(null);
      return;
    }
    setPending(next);
  });

  const displayed = () => {
    void preparationRevision();
    const key = shown();
    return key && isPrepared(key) ? key : null;
  };
  return { keys, presence, pending, displayed, generation, markReady, withdraw };
}

export const SURFACE_SETTINGS = "settings";
export const SURFACE_HOME = "home";

export function stageSurfaceKey(
  projectId: string | null,
  stage: string,
): string {
  return `stage:${projectId ?? "app"}:${stage}`;
}

export function chatsSurfaceKey(projectId: string): string {
  return `chats:${projectId}`;
}

export function projectConfigSurfaceKey(projectId: string): string {
  return `project-config:${projectId}`;
}

export function chatSessionKey(projectId: string, sessionId: string): string {
  return `chat:${projectId}:${sessionId}`;
}

export function parseChatSessionKey(
  key: string,
): { projectId: string; sessionId: string } | null {
  if (!key.startsWith("chat:")) return null;
  const rest = key.slice("chat:".length);
  const split = rest.indexOf(":");
  if (split <= 0 || split === rest.length - 1) return null;
  return { projectId: rest.slice(0, split), sessionId: rest.slice(split + 1) };
}

export function parseStageSurfaceKey(key: string): string | null {
  if (!key.startsWith("stage:")) return null;
  const rest = key.slice("stage:".length);
  const split = rest.indexOf(":");
  if (split <= 0 || split === rest.length - 1) return null;
  return rest.slice(split + 1);
}

export function retainStageSurface(
  key: string,
  projectId: string | null,
): boolean {
  if (key === SURFACE_SETTINGS || key === SURFACE_HOME) return true;
  if (key.startsWith("stage:app:")) return projectId == null;
  if (!projectId) return false;
  return (
    key === chatsSurfaceKey(projectId) ||
    key === projectConfigSurfaceKey(projectId) ||
    key.startsWith(`stage:${projectId}:`)
  );
}

export function retainChatSurface(
  key: string,
  projectId: string | null,
): boolean {
  if (!projectId) return false;
  return key.startsWith(`chat:${projectId}:`);
}
