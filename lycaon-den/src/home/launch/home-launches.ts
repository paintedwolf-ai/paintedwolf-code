import { batch } from "solid-js";
import { createStore, produce, reconcile } from "solid-js/store";
import type { HomeStart, SessionMode } from "../../api/types.ts";
import { afterPaint } from "../../ui/surface-reveal.ts";
import {
  chipsNeedingAction,
  firstPromptAdmitted,
  withHostState,
  type CompositionRecord,
  type HomeOutboxManifest,
  type HostBinding,
  type LaunchChip,
  type LaunchRecord,
} from "./home-launch-model.ts";
import { homeOutbox, type HomeOutbox } from "./home-outbox.ts";
import { markLaunch } from "./home-launch-diagnostics.ts";

/** What currently keeps a launch from progressing, and how the person resolves it. */
export type LaunchBlocker =
  | { code: "engine_offline" }
  | { code: "transport_failed"; message: string }
  | { code: "host_outdated" }
  | { code: "destination_changed" }
  | { code: "attachments"; chips: string[] }
  | { code: "host_failed"; failure: string; resumable: boolean }
  | { code: "chat_failed" }
  | { code: "provider"; message: string }
  | { code: "send_refused"; message: string }
  | { code: "stopped" }
  | { code: "retired" }
  | { code: "unknown"; message: string };

export type LaunchStep =
  | "starting"
  | "creating"
  | "preparing"
  | "attaching"
  | "sending"
  | "sent";

/** Session-local progress; never persisted. */
export type LaunchRuntime = {
  step: LaunchStep;
  blocker?: LaunchBlocker;
  save: "pending" | "saved" | "failed";
  saveError?: string;
  /** Set while a transient stall has lasted long enough to mention. */
  slow?: boolean;
};

type State = {
  loaded: boolean;
  records: Record<string, LaunchRecord>;
  runtime: Record<string, LaunchRuntime>;
  knownHost?: HostBinding;
  foregroundLaunchId?: string;
};

const [state, setState] = createStore<State>({ loaded: false, records: {}, runtime: {} });

/** Launches removed in this window; persistence drops them from the shared manifest. */
const removedHere = new Set<string>();
let compositionToSave: CompositionRecord | null | undefined;
let writeChain: Promise<void> = Promise.resolve();
let outboxOverride: HomeOutbox | undefined;
const outbox = () => outboxOverride ?? homeOutbox();

export const homeLaunches = {
  state,
  record: (launchId: string): LaunchRecord | undefined => state.records[launchId],
  runtime: (launchId: string): LaunchRuntime | undefined => state.runtime[launchId],
  /** Launches still waiting for a first turn, newest first. */
  pending: (): LaunchRecord[] =>
    Object.values(state.records)
      .filter((record) => record.intent !== "discard")
      .sort((a, b) => b.createdAt - a.createdAt),
};

function defaultRuntime(): LaunchRuntime {
  return { step: "starting", save: "pending" };
}

/** Loads saved launches and the last Home composition. */
export async function loadHomeLaunches(): Promise<{ composition?: CompositionRecord; foregroundLaunchId?: string }> {
  const manifest = await outbox().read();
  batch(() => {
    for (const record of manifest.launches) {
      setState("records", record.launchId, record);
      setState("runtime", record.launchId, (prev) => prev ?? { ...defaultRuntime(), save: "saved" });
    }
    setState("knownHost", manifest.knownHost);
    setState("foregroundLaunchId", manifest.foregroundLaunchId);
    setState("loaded", true);
  });
  return { composition: manifest.composition, foregroundLaunchId: manifest.foregroundLaunchId };
}

export type HomeSubmission = {
  text: string;
  mode: SessionMode;
  chips: LaunchChip[];
  compositionId: string;
  compositionRevision: number;
};

/**
 * Creates the launch in memory and returns its id. Persistence and every host
 * call run after the chat paints; onCommitted names the launch to drive once
 * the composition's single launch is decided.
 */
export function submitHomeLaunch(submission: HomeSubmission, onCommitted: (launchId: string) => void): string {
  const launchId = crypto.randomUUID();
  markLaunch(launchId, "submit");
  const record: LaunchRecord = {
    launchId,
    localRevision: 1,
    createdAt: Date.now(),
    compositionId: submission.compositionId,
    compositionRevision: submission.compositionRevision,
    text: submission.text,
    mode: submission.mode,
    chips: submission.chips,
    intent: "send",
    ...(state.knownHost ? { expectedHost: state.knownHost } : {}),
    promptOperationId: launchId,
  };
  batch(() => {
    setState("records", launchId, record);
    setState("runtime", launchId, defaultRuntime());
    setState("foregroundLaunchId", launchId);
  });
  afterPaint(() => {
    markLaunch(launchId, "first_render");
    // Without durable storage the launch still proceeds from memory.
    void persistSubmission(launchId).then(
      (winner) => onCommitted(winner ?? launchId),
      () => onCommitted(launchId),
    );
  });
  return launchId;
}

/**
 * Commits the launch and consumes its composition revision in one manifest
 * write. Another window that already consumed the revision wins; its launch
 * id is returned so this window can show it instead.
 */
async function persistSubmission(launchId: string): Promise<string | undefined> {
  const record = state.records[launchId];
  if (!record) return undefined;
  let winner: string | undefined = launchId;
  await writeManifest((manifest) => {
    const consumed = manifest.launches.find((other) =>
      other.launchId !== launchId &&
      other.compositionId === record.compositionId &&
      other.compositionRevision === record.compositionRevision);
    if (consumed) {
      winner = consumed.launchId;
      return;
    }
    if (manifest.composition?.id === record.compositionId) manifest.composition = undefined;
  }, launchId);
  if (winner !== launchId) {
    batch(() => {
      setState("records", produce((records) => { delete records[launchId]; }));
      setState("runtime", produce((runtime) => { delete runtime[launchId]; }));
    });
    return winner;
  }
  markLaunch(launchId, "local_commit");
  return launchId;
}

/** Applies a local change to one launch and schedules its durable write. */
export function updateLaunch(launchId: string, change: (record: LaunchRecord) => LaunchRecord): void {
  const current = state.records[launchId];
  if (!current) return;
  const next = change(current);
  if (next === current) return;
  setState("records", launchId, reconcile({ ...next, localRevision: Math.max(next.localRevision, current.localRevision + 1) }));
  void writeManifest(() => undefined, launchId);
}

/** Writes the launch again, as after a failed save. */
export function persistLaunch(launchId: string): void {
  if (!state.records[launchId]) return;
  setLaunchRuntime(launchId, { save: "pending" });
  void writeManifest(() => undefined, launchId).catch(() => undefined);
}

export function applyHostState(launchId: string, host: HomeStart): void {
  const current = state.records[launchId];
  if (!current) return;
  const next = withHostState(current, host);
  if (next === current) return;
  setState("records", launchId, reconcile(next));
  if (firstPromptAdmitted(next)) markLaunch(launchId, "admission");
  else if (next.hostState?.session_id && !current.hostState?.session_id) markLaunch(launchId, "binding");
  void writeManifest(() => undefined, launchId);
}

export function setLaunchRuntime(launchId: string, patch: Partial<LaunchRuntime>): void {
  if (!state.records[launchId]) return;
  setState("runtime", launchId, (prev) => ({ ...(prev ?? defaultRuntime()), ...patch }));
}

export function clearLaunchBlocker(launchId: string): void {
  setState("runtime", launchId, produce((runtime) => { delete runtime.blocker; delete runtime.slow; }));
}

export function rememberHost(host: HostBinding): void {
  if (state.knownHost?.storeId === host.storeId && state.knownHost.personId === host.personId) return;
  setState("knownHost", host);
  void writeManifest((manifest) => { manifest.knownHost = host; });
}

export function setForegroundLaunch(launchId: string | undefined): void {
  if (state.foregroundLaunchId === launchId) return;
  setState("foregroundLaunchId", launchId);
  void writeManifest((manifest) => { manifest.foregroundLaunchId = launchId; });
}

/** Removes the launch here and from saved state; its chat, if any, keeps its history. */
export function forgetLaunch(launchId: string): void {
  removedHere.add(launchId);
  batch(() => {
    setState("records", produce((records) => { delete records[launchId]; }));
    setState("runtime", produce((runtime) => { delete runtime[launchId]; }));
    if (state.foregroundLaunchId === launchId) setState("foregroundLaunchId", undefined);
  });
  void writeManifest(() => undefined).then(() => outbox().sweep()).catch(() => undefined);
}

/** Saves the Home composition; the latest revision wins. */
export function saveComposition(composition: CompositionRecord | null): Promise<void> {
  compositionToSave = composition;
  return writeManifest(() => undefined);
}

/** Attachment bytes this window still holds in memory, by item id. */
const heldBytes = new Map<string, Blob>();

/**
 * Keeps a chip's bytes in memory for this session and copies them into the
 * outbox. Launches naming the item become ready once the copy is durable.
 */
export async function stageLaunchBytes(itemId: string, bytes: Blob): Promise<void> {
  heldBytes.set(itemId, bytes);
  await outbox().putBlob(itemId, bytes);
  for (const record of Object.values(state.records)) {
    if (!record.chips.some((chip) => chip.kind === "bytes" && chip.itemId === itemId && chip.snapshot !== "ready")) continue;
    updateLaunch(record.launchId, (r) => ({
      ...r,
      chips: r.chips.map((chip) => chip.kind === "bytes" && chip.itemId === itemId ? { ...chip, snapshot: "ready" } : chip),
    }));
  }
}

export async function readLaunchBytes(itemId: string): Promise<Blob | undefined> {
  return heldBytes.get(itemId) ?? await outbox().readBlob(itemId);
}

export function fingerprintPath(path: string) {
  return outbox().fingerprint(path).catch(() => undefined);
}

/**
 * Merges this window's records into the stored manifest and commits it
 * against the revision it read. Records keep whichever copy has the higher
 * local revision, so a late write never replaces a newer one.
 */
function writeManifest(extra: (manifest: HomeOutboxManifest) => void, savingLaunchId?: string): Promise<void> {
  const run = async () => {
    for (let attempt = 0; attempt < 8; attempt++) {
      const stored = await outbox().read();
      const next = mergeManifest(stored);
      extra(next);
      const outcome = await outbox().commit(next, stored.revision);
      if (outcome.committed) {
        adoptManifest(outcome.manifest);
        return;
      }
    }
    throw new Error("Saved Home ideas kept changing in another window.");
  };
  const write = writeChain.then(run);
  writeChain = write.catch(() => undefined);
  return write.then(
    () => { if (savingLaunchId) setLaunchRuntime(savingLaunchId, { save: "saved", saveError: undefined }); },
    (error: unknown) => {
      if (savingLaunchId) {
        setLaunchRuntime(savingLaunchId, {
          save: "failed",
          saveError: error instanceof Error ? error.message : String(error),
        });
      }
      throw error;
    },
  );
}

function mergeManifest(stored: HomeOutboxManifest): HomeOutboxManifest {
  const byId = new Map(stored.launches.map((record) => [record.launchId, record]));
  for (const id of removedHere) byId.delete(id);
  for (const record of Object.values(state.records)) {
    const other = byId.get(record.launchId);
    if (!other || other.localRevision < record.localRevision ||
      (other.localRevision === record.localRevision && (other.hostState?.revision ?? 0) < (record.hostState?.revision ?? 0))) {
      byId.set(record.launchId, structuredClone(unwrap(record)));
    }
  }
  const next: HomeOutboxManifest = { ...stored, launches: [...byId.values()] };
  if (compositionToSave !== undefined) {
    const stale = compositionToSave && stored.composition?.id === compositionToSave.id &&
      stored.composition.revision > compositionToSave.revision;
    if (!stale) next.composition = compositionToSave ?? undefined;
  }
  return next;
}

function adoptManifest(manifest: HomeOutboxManifest): void {
  compositionToSave = undefined;
  batch(() => {
    for (const record of manifest.launches) {
      if (removedHere.has(record.launchId)) continue;
      const mine = state.records[record.launchId];
      if (!mine || mine.localRevision < record.localRevision) {
        setState("records", record.launchId, reconcile(record));
        setState("runtime", record.launchId, (prev) => prev ?? { ...defaultRuntime(), save: "saved" });
      }
    }
  });
}

function unwrap<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

/** Chips that block sending until the person chooses again or removes them. */
export function launchAttachmentBlockers(record: LaunchRecord): string[] {
  return chipsNeedingAction(record.chips).map((chip) => chip.itemId);
}

export function resetHomeLaunchesForTest(outbox?: HomeOutbox): void {
  outboxOverride = outbox;
  removedHere.clear();
  heldBytes.clear();
  compositionToSave = undefined;
  writeChain = Promise.resolve();
  setState(reconcile({ loaded: false, records: {}, runtime: {} } as State));
}

/** Waits for queued writes; tests use it to observe durable state. */
export function flushHomeLaunchWrites(): Promise<void> {
  return writeChain;
}
