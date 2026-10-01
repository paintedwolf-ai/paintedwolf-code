import { isTauriRuntime } from "../runtime.ts";
import { listenHostEvent } from "./window-channel.ts";
import { claimShortcutBoundary } from "../../shortcuts/dispatcher.ts";

type ExitFlush = () => void | Promise<void>;
type CloseResources = { beforeDestroy: ExitFlush; onKeepOpen: () => void };
type ExitRequest = { request_id: number };
type AppExitAttempt = { id: number; controller: AbortController; releaseInput: () => void };

const flushes = new Set<ExitFlush>();
const closeResources = new Map<ExitFlush, CloseResources>();
let disposeDOMListeners: (() => void) | undefined;
let disposeTauriListener: (() => void) | undefined;
let tauriListenerPending: Promise<void> | undefined;
let listenerRetry: ReturnType<typeof setTimeout> | undefined;
let tauriListenerEpoch = 0;
let closing: AbortController | undefined;
let appExit: AppExitAttempt | undefined;
let flushInFlight: Promise<void> | undefined;
let inputHolds = 0;
let originalInert = false;

function holdInput(): () => void {
  // Native menu commands bypass DOM inertness.
  const releaseCommands = claimShortcutBoundary();
  if (typeof document === "undefined") return releaseCommands;
  const root = document.documentElement;
  if (inputHolds++ === 0) { originalInert = root.inert; root.inert = true; }
  let released = false;
  return () => {
    if (released) return;
    released = true;
    releaseCommands();
    if (--inputHolds === 0) root.inert = originalInert;
  };
}

function flushRegisteredState(): Promise<void> {
  if (flushInFlight) return flushInFlight;
  const run = Promise.allSettled([...flushes].map(async (flush) => flush()))
    .then((results) => {
      const rejected = results.find((result): result is PromiseRejectedResult => result.status === "rejected");
      if (rejected) throw rejected.reason;
    }).finally(() => { if (flushInFlight === run) flushInFlight = undefined; });
  flushInFlight = run;
  return run;
}

function resumeResources(): void {
  for (const resource of closeResources.values()) {
    try { resource.onKeepOpen(); }
    catch (error) { console.error("failed to resume window resources", error); }
  }
}

async function releaseResources(): Promise<void> {
  const results = await Promise.allSettled([...closeResources.values()].map(async (resource) => resource.beforeDestroy()));
  const failed = results.find((result): result is PromiseRejectedResult => result.status === "rejected");
  if (failed) throw failed.reason;
}

function retryDelay(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const finish = () => { clearTimeout(timer); signal.removeEventListener("abort", finish); resolve(); };
    const timer = setTimeout(finish, milliseconds);
    signal.addEventListener("abort", finish, { once: true });
    if (signal.aborted) finish();
  });
}

// A failed preservation keeps the window and its work alive. Retry the whole
// boundary, including local state changed before a preceding attempt settled.
async function preserveForExit(finish: () => Promise<void>, signal: AbortSignal): Promise<void> {
  let failures = 0;
  while (!signal.aborted) {
    try {
      // A hide flush may have captured state before input was frozen. Drain it
      // before taking the final snapshot promised to the native coordinator.
      if (flushInFlight) await flushInFlight;
      if (signal.aborted) return;
      await flushRegisteredState();
      if (signal.aborted) return;
      await releaseResources();
      if (signal.aborted) { resumeResources(); return; }
      await finish();
      return;
    } catch (error) {
      resumeResources();
      console.error("failed to finish durable application exit; retrying", error);
      await retryDelay(Math.min(30_000, 1_500 * 2 ** Math.min(failures++, 5)), signal);
    }
  }
}

function cancelApplicationExit(request: ExitRequest): void {
  if (appExit?.id !== request.request_id) return;
  const attempt = appExit;
  appExit = undefined;
  attempt.controller.abort();
  attempt.releaseInput();
  resumeResources();
}

async function followApplicationExit(attempt: AppExitAttempt): Promise<void> {
  while (!attempt.controller.signal.aborted) {
    await retryDelay(1_500, attempt.controller.signal);
    if (attempt.controller.signal.aborted) return;
    try {
      const { invoke } = await import("@tauri-apps/api/core");
      const pending = await invoke<ExitRequest | null>("pending_app_exit");
      if (attempt.controller.signal.aborted) return;
      if (!pending) cancelApplicationExit({ request_id: attempt.id });
      else if (pending.request_id !== attempt.id) void prepareApplicationExit(pending);
    } catch {
      // IPC can end during exit; input stays frozen after preservation.
    }
  }
}

async function prepareApplicationExit(request: ExitRequest): Promise<void> {
  if (appExit?.id === request.request_id) return;
  if (appExit) cancelApplicationExit({ request_id: appExit.id });
  const attempt: AppExitAttempt = { id: request.request_id, controller: new AbortController(), releaseInput: holdInput() };
  appExit = attempt;
  void followApplicationExit(attempt);
  await preserveForExit(async () => {
    const { invoke } = await import("@tauri-apps/api/core");
    if (!attempt.controller.signal.aborted) await invoke("acknowledge_app_exit", { requestId: attempt.id });
  }, attempt.controller.signal);
  // Keep input frozen after acknowledgement until native exit, or cancellation
  // of a failed installation. Other windows may still be preserving their work.
}

function installDOMListeners(): void {
  if (typeof document === "undefined" || typeof window === "undefined") return;
  const flush = () => void flushRegisteredState().catch((error: unknown) => console.error("failed to flush durable app state", error));
  const onVisibility = () => { if (document.visibilityState === "hidden") flush(); };
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("pagehide", flush);
  window.addEventListener("beforeunload", flush);
  disposeDOMListeners = () => {
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("pagehide", flush);
    window.removeEventListener("beforeunload", flush);
  };
}

function installTauriCloseListener(): void {
  if (!isTauriRuntime() || tauriListenerPending || disposeTauriListener) return;
  clearTimeout(listenerRetry);
  const epoch = ++tauriListenerEpoch;
  tauriListenerPending = import("@tauri-apps/api/window").then(async ({ getCurrentWindow }) => {
    if (flushes.size === 0 || epoch !== tauriListenerEpoch) return;
    const appWindow = getCurrentWindow();
    const releases: Array<() => void> = [];
    const dispose = () => { for (const release of releases) release(); };
    try {
      releases.push(await appWindow.onCloseRequested(async (event) => {
        event.preventDefault();
        if (closing || appExit) return;
        const controller = new AbortController();
        closing = controller;
        const releaseInput = holdInput();
        try { await preserveForExit(() => appWindow.destroy(), controller.signal); }
        finally {
          releaseInput();
          if (closing === controller) closing = undefined;
        }
      }));
      releases.push(await listenHostEvent<ExitRequest>("app-exit-requested", ({ payload }) => { void prepareApplicationExit(payload); }));
      releases.push(await listenHostEvent<ExitRequest>("app-exit-cancelled", ({ payload }) => cancelApplicationExit(payload)));
      if (flushes.size === 0 || epoch !== tauriListenerEpoch) { dispose(); return; }
      disposeTauriListener = dispose;
      const { invoke } = await import("@tauri-apps/api/core");
      const pending = await invoke<ExitRequest | null>("pending_app_exit");
      if (pending && flushes.size > 0 && epoch === tauriListenerEpoch) void prepareApplicationExit(pending);
    } catch (error) {
      dispose();
      if (disposeTauriListener === dispose) disposeTauriListener = undefined;
      throw error;
    }
  }).catch((error: unknown) => console.error("failed to install durable window-exit handlers; retrying", error))
    .finally(() => {
      tauriListenerPending = undefined;
      if (flushes.size > 0 && !disposeTauriListener) listenerRetry = setTimeout(installTauriCloseListener, 1_500);
    });
}

export function watchWindowExit(flush: ExitFlush, resources?: CloseResources): () => void {
  flushes.add(flush);
  if (resources) closeResources.set(flush, resources);
  if (!disposeDOMListeners) installDOMListeners();
  installTauriCloseListener();
  return () => {
    flushes.delete(flush);
    closeResources.delete(flush);
    if (flushes.size !== 0) return;
    tauriListenerEpoch += 1;
    clearTimeout(listenerRetry);
    closing?.abort();
    closing = undefined;
    if (appExit) cancelApplicationExit({ request_id: appExit.id });
    disposeDOMListeners?.();
    disposeDOMListeners = undefined;
    disposeTauriListener?.();
    disposeTauriListener = undefined;
  };
}
