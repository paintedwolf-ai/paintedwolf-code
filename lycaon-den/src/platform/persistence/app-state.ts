import { clientIdentity } from "../connection/client-identity.ts";
import {
  APP_STATE_VERSION,
  EMPTY_APP_STATE_V1,
  type AppStatePatch,
  type DenAppStateV1,
} from "../../../shared/app-state-types.ts";
import {
  APP_STATE_STORAGE_SLICE_PREFIX,
  appStateStorageSliceKey,
} from "../../../shared/app-state-storage.ts";
import { parseAppState } from "./app-state-parse.ts";
import { isTauriRuntime } from "../runtime.ts";

function readLocalDoc(): Record<string, unknown> {
  const doc: Record<string, unknown> = { version: APP_STATE_VERSION };
  try {
    for (let index = 0; index < localStorage.length; index += 1) {
      const storageKey = localStorage.key(index);
      if (!storageKey?.startsWith(APP_STATE_STORAGE_SLICE_PREFIX)) continue;
      const encoded = storageKey.slice(APP_STATE_STORAGE_SLICE_PREFIX.length);
      const key = decodeURIComponent(encoded);
      if (!key || key === "version") continue;
      const raw = localStorage.getItem(storageKey);
      if (raw === null) continue;
      try {
        doc[key] = JSON.parse(raw);
      } catch {
        // Corruption stays isolated to this slice.
      }
    }
  } catch {
    // Storage may be unavailable in a locked-down browser context.
  }
  return doc;
}

/** Loads persisted app state. */
export async function loadAppState(): Promise<DenAppStateV1> {
  if (isTauriRuntime()) {
    try {
      const { invoke } = await import("@tauri-apps/api/core");
      const raw = await invoke<unknown>("read_app_state");
      return parseAppState(raw);
    } catch {
      return { ...EMPTY_APP_STATE_V1 };
    }
  }
  try {
    return parseAppState(readLocalDoc());
  } catch {
    return { ...EMPTY_APP_STATE_V1 };
  }
}

const WRITE_APP_STATE_TIMEOUT_MS = 4000;

function withPatchTimeout<T>(promise: Promise<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error("app state write timed out")),
      WRITE_APP_STATE_TIMEOUT_MS,
    );
    void Promise.resolve(promise)
      .then((v) => {
        clearTimeout(timer);
        resolve(v);
      })
      .catch((error: unknown) => {
        clearTimeout(timer);
        reject(error instanceof Error ? error : new Error(String(error)));
      });
  });
}

export async function patchAppState(
  patch: AppStatePatch,
  optimisticState: DenAppStateV1,
): Promise<DenAppStateV1> {
  if (isTauriRuntime()) {
    const { invoke } = await import("@tauri-apps/api/core");
    await withPatchTimeout(invoke<void>("patch_app_state", { patch }));
    return optimisticState;
  }

  for (const [key, requested] of Object.entries(patch)) {
    if (key === "version") continue;
    let value = requested;
    if (key === "filesTreeIntent" && value) {
      const current = parseAppState(readLocalDoc()).filesTreeIntent;
      const incoming = parseAppState({ version: APP_STATE_VERSION, filesTreeIntent: value }).filesTreeIntent;
      const identity = clientIdentity();
      const windowIntent = incoming?.byWindow[identity];
      if (!windowIntent) throw new Error("Missing window tree configuration.");
      value = { byWindow: { ...current?.byWindow, [identity]: windowIntent } };
    }
    const storageKey = appStateStorageSliceKey(key);
    if (value === null) localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, JSON.stringify(value));
  }
  return parseAppState({ ...readLocalDoc(), version: optimisticState.version });
}
