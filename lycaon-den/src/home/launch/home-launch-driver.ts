import type { LycaonClient } from "../../api/client.ts";
import { isApiErrorCode, LycaonApiError } from "../../api/http.ts";
import type { HomeStart, PromptAttachmentPart, PromptReferencePart } from "../../api/types.ts";
import { attachmentReceiptToPending, attachmentRejectCode, fileToComposerAttachment, isSendablePendingAttachment, mediaMimeFromPath, nativeProjectionMimeFromPath, rejectMessageForCode, type ComposerPendingAttachment } from "../../chat/composer/composer-attachments.ts";
import { promptPartsFromPendingAttachments } from "../../chat/composer/prompt-parts.ts";
import { importExternalAttachment } from "../../platform/files/external-attachment-import.ts";
import {
  boundChat,
  firstPromptAdmitted,
  sameHost,
  type FirstPromptRequest,
  type LaunchChip,
  type LaunchRecord,
} from "./home-launch-model.ts";
import {
  applyHostState,
  clearLaunchBlocker,
  fingerprintPath,
  homeLaunches,
  launchAttachmentBlockers,
  readLaunchBytes,
  rememberHost,
  setLaunchRuntime,
  updateLaunch,
  type LaunchBlocker,
} from "./home-launches.ts";
import { noteLaunchFailure } from "./home-launch-diagnostics.ts";

export type LaunchDriverDeps = {
  /** The connected client, or null while the engine is unreachable. */
  client: () => LycaonClient | null;
  /** Calls listener whenever the connection changes. */
  onConnectionChange: (listener: () => void) => () => void;
  /** Opens the bound chat once its first turn is admitted. */
  handoff: (launchId: string, chat: { projectId: string; sessionId: string }, followUp?: string) => void;
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
};

const RETRY_DELAYS_MS = [1_000, 2_000, 4_000];
const POLL_START_MS = 100;
const POLL_CAP_MS = 5_000;
/** A wait this long is worth mentioning in the chat. */
const SLOW_AFTER_MS = 4_000;

type Driver = { controller: AbortController; wake: () => void };
const drivers = new Map<string, Driver>();
let deps: LaunchDriverDeps | undefined;
let unsubscribeConnection: (() => void) | undefined;

export function configureLaunchDriver(next: LaunchDriverDeps): void {
  deps = next;
  unsubscribeConnection?.();
  unsubscribeConnection = next.onConnectionChange(() => {
    for (const driver of drivers.values()) driver.wake();
  });
}

/** Starts the launch's driver unless one is already running. */
export function driveLaunch(launchId: string): void {
  if (!deps || drivers.has(launchId)) return;
  const controller = new AbortController();
  let wakeWaiter: (() => void) | undefined;
  const driver: Driver = {
    controller,
    wake: () => { const waiter = wakeWaiter; wakeWaiter = undefined; waiter?.(); },
  };
  drivers.set(launchId, driver);
  const waitForWake = () => new Promise<void>((resolve) => {
    wakeWaiter = resolve;
    controller.signal.addEventListener("abort", () => resolve(), { once: true });
  });
  void runLaunch(launchId, deps, controller.signal, waitForWake)
    .catch((error: unknown) => {
      if (controller.signal.aborted) return;
      const message = error instanceof Error ? error.message : String(error);
      noteLaunchFailure(launchId, "driver");
      setLaunchRuntime(launchId, { blocker: { code: "unknown", message } });
    })
    .finally(() => {
      if (drivers.get(launchId) === driver) drivers.delete(launchId);
    });
}

/** Re-runs a launch after the person acted on its blocker. */
export function wakeLaunch(launchId: string): void {
  const driver = drivers.get(launchId);
  if (driver) driver.wake();
  else driveLaunch(launchId);
}

/** Cancels the launch's transport; its saved intent decides what follows. */
export function haltLaunch(launchId: string): void {
  drivers.get(launchId)?.controller.abort();
  drivers.delete(launchId);
}

function defaultSleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, { once: true });
  });
}

class Offline extends Error {}

/** Network failures and gateway refusals that a repeat can fix. */
function transient(error: unknown): boolean {
  if (error instanceof LycaonApiError) return (error.status ?? 0) >= 500 || error.status === 429;
  return !(error instanceof DOMException && error.name === "AbortError");
}

async function runLaunch(
  launchId: string,
  deps: LaunchDriverDeps,
  signal: AbortSignal,
  waitForWake: () => Promise<void>,
): Promise<void> {
  const sleep = (ms: number) => (deps.sleep ?? defaultSleep)(ms, signal);
  const record = () => homeLaunches.record(launchId);
  const block = async (blocker: LaunchBlocker) => {
    noteLaunchFailure(launchId, blocker.code);
    setLaunchRuntime(launchId, { blocker });
    await waitForWake();
    clearLaunchBlocker(launchId);
  };
  const connected = (): LycaonClient => {
    const client = deps.client();
    if (!client) throw new Offline();
    return client;
  };
  /** One request with bounded retries; a new connection or Retry starts over. */
  const request = async <T>(call: (client: LycaonClient) => Promise<T>): Promise<T> => {
    for (let attempt = 0; ; attempt++) {
      const client = connected();
      try {
        return await call(client);
      } catch (error) {
        if (signal.aborted) throw error;
        if (!transient(error)) throw error;
        if (deps.client() !== client) continue;
        const delay = RETRY_DELAYS_MS[attempt];
        if (delay === undefined) {
          await block({ code: "transport_failed", message: error instanceof Error ? error.message : String(error) });
          attempt = -1;
          continue;
        }
        await sleep(delay);
      }
    }
  };
  /** Polls until the host view changes or stops waiting, at most one read at a time. */
  const follow = async (until: (state: HomeStart) => boolean) => {
    const started = Date.now();
    let delay = POLL_START_MS;
    for (;;) {
      const state = await request((client) => client.getHomeStart(launchId));
      if (signal.aborted) return;
      applyHostState(launchId, state);
      if (until(state)) {
        setLaunchRuntime(launchId, { slow: false });
        return;
      }
      setLaunchRuntime(launchId, { slow: Date.now() - started >= SLOW_AFTER_MS });
      await sleep(delay);
      delay = Math.min(delay * 2, POLL_CAP_MS);
    }
  };

  while (!signal.aborted) {
    const current = record();
    if (!current || current.intent === "discard") return;
    try {
      if (current.intent === "stop") {
        const stopped = await request((client) => client.stopHomeStart(launchId));
        applyHostState(launchId, stopped);
        const after = record();
        if (after && firstPromptAdmitted(after)) return handoff(after, deps);
        await block({ code: "stopped" });
        continue;
      }
      const client = connected();
      if (!(await bindHost(launchId, client, block))) continue;
      const hostState = record()?.hostState;
      if (!hostState || (hostState.phase === "failed" && hostState.failure?.resumable && current.localRevision > 0 && !current.sendAttempted && needsResume(current))) {
        setLaunchRuntime(launchId, { step: "creating" });
        const reserved = await request((c) => c.reserveHomeStart(launchId, { choices: { mode: current.mode } }));
        applyHostState(launchId, reserved);
      }
      const reserved = record()?.hostState;
      if (!reserved) continue;
      if (reserved.phase === "stopped") { await block({ code: "stopped" }); continue; }
      if (reserved.phase === "retired") { await block({ code: "retired" }); continue; }
      if (reserved.phase === "failed") {
        await block({ code: "host_failed", failure: reserved.failure?.code ?? "unknown", resumable: reserved.failure?.resumable ?? false });
        if (reserved.failure?.resumable) updateLaunch(launchId, (r) => ({ ...r, hostState: undefined }));
        continue;
      }
      if (reserved.phase !== "session_bound") {
        setLaunchRuntime(launchId, { step: "creating" });
        await follow((state) => state.phase !== "reserved" && state.phase !== "project_created");
        continue;
      }
      if (reserved.session_status === "preparing") {
        setLaunchRuntime(launchId, { step: "preparing" });
        await follow((state) => state.session_status !== "preparing" || state.phase !== "session_bound");
        continue;
      }
      const bound = record();
      if (!bound) return;
      if (firstPromptAdmitted(bound)) return handoff(bound, deps);
      if (reserved.session_status === "error") { await block({ code: "chat_failed" }); continue; }
      if (bound.proposedText !== undefined && bound.sendAttempted) {
        await replaceFirstPrompt(launchId, request, deps);
        continue;
      }
      if (launchAttachmentBlockers(bound).length > 0) {
        await block({ code: "attachments", chips: launchAttachmentBlockers(bound) });
        continue;
      }
      if (!bound.firstPrompt) {
        setLaunchRuntime(launchId, { step: "attaching" });
        const prepared = await prepareFirstPrompt(bound, connected());
        if (prepared.blocked) continue;
        updateLaunch(launchId, (r) => ({ ...r, firstPrompt: prepared.request }));
        continue;
      }
      setLaunchRuntime(launchId, { step: "sending" });
      if (bound.sendAttempted) {
        const known = await request((c) => c.getHomeStart(launchId));
        applyHostState(launchId, known);
        const latest = record();
        if (latest && firstPromptAdmitted(latest)) return handoff(latest, deps);
      }
      updateLaunch(launchId, (r) => ({ ...r, sendAttempted: true }));
      const chat = boundChat(bound)!;
      const prompt = bound.firstPrompt;
      try {
        await request((c) => c.sendPrompt(chat.sessionId, {
          operation_id: bound.promptOperationId,
          mode: bound.mode,
          text: prompt.text,
          ...(prompt.attachments.length ? { attachments: prompt.attachments as PromptAttachmentPart[] } : {}),
          ...(prompt.references.length ? { references: prompt.references as PromptReferencePart[] } : {}),
        }));
      } catch (error) {
        if (isApiErrorCode(error, ["session_preparing"])) { await sleep(POLL_START_MS); continue; }
        if (isApiErrorCode(error, ["home_start_closed", "home_start_conflict"])) {
          applyHostState(launchId, await request((c) => c.getHomeStart(launchId)));
          continue;
        }
        throw error;
      }
      setLaunchRuntime(launchId, { step: "sent" });
      await follow((state) => state.first_prompt.admitted || state.phase !== "session_bound");
    } catch (error) {
      if (signal.aborted) return;
      if (error instanceof Offline) {
        await block({ code: "engine_offline" });
        continue;
      }
      if (error instanceof LycaonApiError) {
        if (isProviderRefusal(error)) {
          await block({ code: "provider", message: error.message });
          continue;
        }
        await block({ code: "send_refused", message: error.message });
        continue;
      }
      throw error;
    }
  }
}

function needsResume(record: LaunchRecord): boolean {
  return record.hostState?.phase === "failed" && record.hostState.failure?.resumable === true;
}

/** Codes whose remedy is in provider settings. */
function isProviderRefusal(error: LycaonApiError): boolean {
  return String(error.code).startsWith("provider_") || String(error.code).startsWith("model_");
}

/**
 * Binds the launch to the host it reaches first. A launch never replays into
 * another store or person; one composed for a different host waits for the
 * person to send it here.
 */
async function bindHost(
  launchId: string,
  client: LycaonClient,
  block: (blocker: LaunchBlocker) => Promise<void>,
): Promise<boolean> {
  let capabilities;
  try {
    capabilities = await client.getHomeStartCapabilities();
  } catch (error) {
    if (error instanceof LycaonApiError && error.status === 404) {
      await block({ code: "host_outdated" });
      return false;
    }
    throw error;
  }
  const host = { storeId: capabilities.store_id, personId: capabilities.person_id };
  rememberHost(host);
  const record = homeLaunches.record(launchId);
  if (!record) return false;
  if (record.host) {
    if (sameHost(record.host, host)) return true;
    await block({ code: "destination_changed" });
    return false;
  }
  if (record.expectedHost && !sameHost(record.expectedHost, host) && !record.destinationAccepted) {
    await block({ code: "destination_changed" });
    return false;
  }
  updateLaunch(launchId, (r) => ({ ...r, host }));
  return true;
}

async function replaceFirstPrompt(
  launchId: string,
  request: <T>(call: (client: LycaonClient) => Promise<T>) => Promise<T>,
  deps: LaunchDriverDeps,
): Promise<void> {
  const record = homeLaunches.record(launchId);
  if (!record?.hostState || record.proposedText === undefined) return;
  const replacement = crypto.randomUUID();
  try {
    const replaced = await request((c) => c.reissueHomeStartFirstPrompt(launchId, {
      expected_revision: record.hostState!.revision,
      operation_id: record.promptOperationId,
      replacement_operation_id: replacement,
    }));
    updateLaunch(launchId, (r) => ({
      ...r,
      hostState: replaced,
      promptOperationId: replaced.first_prompt.operation_id,
      text: r.proposedText ?? r.text,
      proposedText: undefined,
      firstPrompt: undefined,
      sendAttempted: false,
    }));
  } catch (error) {
    if (isApiErrorCode(error, ["home_start_admitted"])) {
      // The original turn ran; the edit becomes the chat's next message.
      applyHostState(launchId, await request((c) => c.getHomeStart(launchId)));
      const latest = homeLaunches.record(launchId);
      if (latest && firstPromptAdmitted(latest)) handoff(latest, deps, latest.proposedText);
      return;
    }
    if (isApiErrorCode(error, ["home_start_conflict"])) {
      applyHostState(launchId, await request((c) => c.getHomeStart(launchId)));
      return;
    }
    throw error;
  }
}

function handoff(record: LaunchRecord, deps: LaunchDriverDeps, followUp?: string): void {
  const chat = boundChat(record);
  if (!chat) return;
  deps.handoff(record.launchId, chat, followUp);
}

type Prepared = { blocked: false; request: FirstPromptRequest } | { blocked: true };

/**
 * Uploads the launch's attachments and captures the exact first prompt. A chip
 * whose bytes are gone or whose source changed blocks the send until the
 * person chooses again; unaffected chips and text are kept.
 */
async function prepareFirstPrompt(record: LaunchRecord, client: LycaonClient): Promise<Prepared> {
  const chat = boundChat(record)!;
  const staged: ComposerPendingAttachment[] = [];
  const changed: Partial<Record<string, LaunchChip>> = {};
  for (const chip of record.chips) {
    if (chip.kind === "reject") continue;
    if (chip.kind === "bytes") {
      const bytes = await readLaunchBytes(chip.itemId);
      if (!bytes) { changed[chip.itemId] = { ...chip, snapshot: "missing" }; continue; }
      const file = new File([bytes], chip.name, { type: chip.mime });
      staged.push(await fileToComposerAttachment(file, chat.projectId, client));
      continue;
    }
    if (chip.fingerprint) {
      const now = await fingerprintPath(chip.absolutePath);
      if (!now) { changed[chip.itemId] = { ...chip, snapshot: "missing" }; continue; }
      if (now.size !== chip.fingerprint.size || now.modifiedMs !== chip.fingerprint.modifiedMs) {
        changed[chip.itemId] = { ...chip, snapshot: "changed" };
        continue;
      }
    }
    const reference: ComposerPendingAttachment = {
      id: chip.itemId, kind: "external-file", projectId: chat.projectId,
      path: chip.absolutePath, name: chip.name, mediaType: mediaMimeFromPath(chip.absolutePath),
    };
    if (!nativeProjectionMimeFromPath(chip.absolutePath)) { staged.push(reference); continue; }
    try {
      const receipt = await importExternalAttachment(chat.projectId, chip.absolutePath);
      reference.mediaType = receipt.mime;
      const payload = attachmentReceiptToPending(receipt, { importedCopy: true });
      staged.push(...(receipt.kind === "document" || receipt.kind === "video" ? [payload, reference] : [reference]));
    } catch (error) {
      const code = attachmentRejectCode(error);
      changed[chip.itemId] = { ...chip, snapshot: code === "attachment_unavailable" ? "missing" : "changed" };
      noteLaunchFailure(record.launchId, rejectMessageForCode(code) ? code : "attachment_import");
    }
  }
  const refusals = staged.filter((attachment) => !isSendablePendingAttachment(attachment));
  if (Object.keys(changed).length > 0 || refusals.length > 0) {
    updateLaunch(record.launchId, (r) => ({
      ...r,
      chips: r.chips.map((chip) => changed[chip.itemId] ?? chip),
    }));
    if (refusals.length > 0) {
      setLaunchRuntime(record.launchId, {
        blocker: { code: "send_refused", message: refusals.map((attachment) => attachment.name).join(", ") },
      });
    }
    return { blocked: true };
  }
  const parts = promptPartsFromPendingAttachments(staged);
  return {
    blocked: false,
    request: {
      text: record.text,
      attachmentLabels: staged.map((attachment) => attachment.name),
      attachments: (parts.attachments ?? []) as FirstPromptRequest["attachments"],
      references: (parts.references ?? []) as FirstPromptRequest["references"],
    },
  };
}
