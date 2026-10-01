import { getCurrentWindow } from "@tauri-apps/api/window";
import { isTauriRuntime } from "../runtime.ts";
const STORAGE_KEY = "den.client-identity";

let identity: string | undefined;

function stored(): string | null {
  try {
    return sessionStorage.getItem(STORAGE_KEY);
  } catch {
    // A window that refuses session storage keeps its identity in memory.
    return null;
  }
}

function remember(value: string): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, value);
  } catch {
    // This document retains the identity in memory.
  }
}

/** The desktop window label, or null in a browser on the Tauri dev server. */
function tauriWindowLabel(): string | null {
  if (!isTauriRuntime()) return null;
  try {
    return getCurrentWindow().label;
  } catch {
    return null;
  }
}

/** Identifies this window for host events and document outboxes across reloads. */
export function clientIdentity(): string {
  if (identity) return identity;
  const previous = stored()?.trim();
  const label = tauriWindowLabel();
  identity = label !== null ? `window:${label}` : previous?.startsWith("browser:") ? previous : `browser:${crypto.randomUUID()}`;
  remember(identity);
  return identity;
}

let prepared: Promise<string> | undefined;

/** Browser locks give duplicated tabs separate outbox identities. */
export function prepareClientIdentity(): Promise<string> {
  if (prepared) return prepared;
  if (isTauriRuntime() || typeof navigator === "undefined" || !navigator.locks) return Promise.resolve(clientIdentity());
  prepared = new Promise<string>((resolve, reject) => {
    const acquire = (candidate: string): void => {
      void navigator.locks.request(`editor-client:${candidate}`, { ifAvailable: true }, async (lock) => {
        if (!lock) { acquire(`browser:${crypto.randomUUID()}`); return; }
        identity = candidate;
        remember(candidate);
        resolve(candidate);
        await new Promise<void>(() => undefined);
      }).catch(reject);
    };
    acquire(clientIdentity());
  });
  return prepared;
}
