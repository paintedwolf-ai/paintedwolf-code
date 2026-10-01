import { invoke } from "@tauri-apps/api/core";
import { isTauriRuntime, waitForTauriRuntime } from "../runtime.ts";
import {
  beginEngineStartupObservation,
  clearEngineStartupProgress,
} from "./engine-startup.ts";

export type BackendConnection = {
  baseUrl: string;
  apiToken: string;
};

export type SidecarConnectionStatus = "connected" | "offline";

import type { HealthResponse } from "../../api/types.ts";

export type { HealthResponse };

type SidecarInfo = {
  port: number;
  api_token: string;
};

const DEFAULT_DEV_BASE = "http://127.0.0.1:8787";
const DEV_PROBE_ATTEMPTS = 10;
const DEV_PROBE_INTERVAL_MS = 500;

let cachedConnection: BackendConnection | null = null;

function mapSidecarInfo(info: SidecarInfo): BackendConnection {
  return {
    baseUrl: `http://127.0.0.1:${info.port}`,
    apiToken: info.api_token,
  };
}

export async function probeHealth(baseUrl: string, signal?: AbortSignal): Promise<boolean> {
  try {
    // Recovery health still proves reachability.
    const status = (await fetchHealth(baseUrl, signal)).status;
    return status === "ok" || status === "recovery";
  } catch {
    return false;
  }
}

/** Fetch unauthenticated GET /health. */
export async function fetchHealth(baseUrl: string, signal?: AbortSignal): Promise<HealthResponse> {
  const res = await fetch(`${baseUrl.replace(/\/$/, "")}/health`, { cache: "no-store", signal });
  if (!res.ok) {
    throw new Error(`Health check failed (${res.status})`);
  }
  return (await res.json()) as HealthResponse;
}

async function discoverDevBackend(): Promise<BackendConnection> {
  const token = import.meta.env.VITE_LYCAON_API_TOKEN?.trim();
  if (token) {
    // Proxy mode uses an empty base URL to keep requests same-origin.
    const baseUrl =
      import.meta.env.VITE_LYCAON_PROXY === "1"
        ? ""
        : import.meta.env.VITE_LYCAON_API_URL?.trim() || DEFAULT_DEV_BASE;
    // Retry through backend startup and dependency optimization reloads.
    for (let attempt = 0; attempt < DEV_PROBE_ATTEMPTS; attempt++) {
      if (await probeHealth(baseUrl)) {
        return { baseUrl: baseUrl.replace(/\/$/, ""), apiToken: token };
      }
      await new Promise((r) => setTimeout(r, DEV_PROBE_INTERVAL_MS));
    }
  }
  throw new Error(
    "Backend unavailable — start ./task den:sidecar, then ./task den:dev",
  );
}

export type SidecarStartFailure =
  | "store_locked"
  | "engine_not_started"
  | "cancelled"
  | "credential_vault_locked"
  | "credential_vault_uninitialized"
  | "credential_vault_unlock_failed"
  | "credential_vault_corrupt";

/** Mirrors the `SidecarStartError` tag in `sidecar.rs`. */
type TaggedStartError = { code: string; message?: string };

let lastStartFailure: SidecarStartFailure | null = null;
let lastStartDetail: string | null = null;
let nextCredentialVaultPassword: string | null = null;

/** Supplies one app password to the next managed engine start attempt. */
export function provideCredentialVaultPassword(password: string): void {
  nextCredentialVaultPassword = password;
}

/** Permanently removes the encrypted credential vault after UI confirmation. */
export async function resetCredentialVault(): Promise<void> {
  if (!isTauriRuntime()) {
    throw new Error("Credential vault reset is available only in the desktop app.");
  }
  clearBackendConnection();
  nextCredentialVaultPassword = null;
  await invoke<void>("reset_credential_vault");
}

export function lastSidecarStartFailure(): SidecarStartFailure | null {
  return lastStartFailure;
}

export function lastSidecarStartDetail(): string | null {
  return lastStartDetail;
}

function taggedStartError(err: unknown): TaggedStartError | null {
  if (!err || typeof err !== "object" || err instanceof Error) return null;
  const code = (err as TaggedStartError).code;
  return typeof code === "string" ? (err as TaggedStartError) : null;
}

function errorMessage(err: unknown): string {
  // Tagged host errors carry their display message as data.
  const tagged = taggedStartError(err);
  if (tagged) return tagged.message ?? tagged.code;
  return err instanceof Error ? err.message : String(err);
}

/** Record a machine-readable reason when the host gave us one. */
function noteStartFailure(err: unknown): void {
  const tagged = taggedStartError(err);
  if (!tagged) return;
  if (
    tagged.code === "store_locked" ||
    tagged.code === "engine_not_started" ||
    tagged.code === "cancelled" ||
    tagged.code === "credential_vault_locked" ||
    tagged.code === "credential_vault_uninitialized" ||
    tagged.code === "credential_vault_unlock_failed" ||
    tagged.code === "credential_vault_corrupt"
  ) {
    lastStartFailure = tagged.code;
  }
  if (tagged.message?.trim()) lastStartDetail = tagged.message.trim();
}

async function discoverTauriBackend(): Promise<BackendConnection> {
  let lastError = "Could not connect to the backend";
  // Reset details before each start attempt.
  lastStartFailure = null;
  lastStartDetail = null;

  try {
    const password = nextCredentialVaultPassword;
    nextCredentialVaultPassword = null;
    const info = await invokeManagedSidecar("start_sidecar", password);
    return mapSidecarInfo(info);
  } catch (err) {
    noteStartFailure(err);
    lastError = errorMessage(err);
  }

  if (import.meta.env.DEV) {
    try {
      const attached = await invoke<SidecarInfo | null>("attach_existing_daemon");
      if (attached) {
        return mapSidecarInfo(attached);
      }
    } catch (err) {
      lastError = errorMessage(err);
    }

    try {
      return await discoverDevBackend();
    } catch (err) {
      lastError = errorMessage(err);
    }
  }

  throw new Error(lastError);
}

async function invokeManagedSidecar(
  command: "start_sidecar" | "restart_sidecar",
  password: string | null = null,
): Promise<SidecarInfo> {
  const stopObserving = await beginEngineStartupObservation();
  try {
    return command === "start_sidecar"
      ? await invoke<SidecarInfo>(command, { password })
      : await invoke<SidecarInfo>(command);
  } finally {
    stopObserving();
    clearEngineStartupProgress();
  }
}

/** Resolve the loopback endpoint and bearer. */
export async function discoverBackend(): Promise<BackendConnection> {
  const inTauri = await waitForTauriRuntime();
  const connection = inTauri
    ? await discoverTauriBackend()
    : await discoverDevBackend();
  cachedConnection = connection;
  return connection;
}

export function getBackendConnection(): BackendConnection | null {
  return cachedConnection;
}

/** Register a known connection without spawning (peer views, tests). */
export function setBackendConnection(connection: BackendConnection): void {
  cachedConnection = connection;
}

export function clearBackendConnection(): void {
  cachedConnection = null;
}

/** Read-only sidecar probe — never starts the engine. */
export async function readSidecarInfo(): Promise<BackendConnection | null> {
  if (!isTauriRuntime()) return null;
  // Missing and unreadable manifests both report offline.
  try {
    const info = await invoke<SidecarInfo | null>("sidecar_info");
    return info ? mapSidecarInfo(info) : null;
  } catch {
    return null;
  }
}

/** Kill and respawn sidecar; rotates bearer in bundled path. */
export async function restartBackend(): Promise<BackendConnection> {
  clearBackendConnection();
  if (!isTauriRuntime()) {
    return discoverDevBackend();
  }
  const info = await invokeManagedSidecar("restart_sidecar");
  const connection = mapSidecarInfo(info);
  cachedConnection = connection;
  return connection;
}

/** Ask the managed child to stop its current startup attempt. */
export async function cancelBackendStart(): Promise<boolean> {
  if (!isTauriRuntime()) return false;
  return invoke<boolean>("cancel_sidecar_start");
}
/** Same-origin harness control calls. */
export async function harnessControlFetch(
  path: string,
  init?: RequestInit,
): Promise<Record<string, unknown>> {
  const token = getBackendConnection()?.apiToken ?? "";
  // Normalize every HeadersInit shape before merging.
  const headers = new Headers({
    Authorization: `Bearer ${token}`,
    "Content-Type": "application/json",
  });
  new Headers(init?.headers).forEach((value, key) => headers.set(key, value));
  const res = await fetch(path, { ...init, headers });
  const body = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  if (!res.ok) return { ok: false, status: res.status, ...body };
  return body;
}
