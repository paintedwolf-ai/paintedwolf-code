import { invoke } from "@tauri-apps/api/core";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { emptyManifest, reachableItemIds, type HomeOutboxManifest, type PathFingerprint } from "./home-launch-model.ts";

export type CommitOutcome =
  | { committed: true; manifest: HomeOutboxManifest }
  | { committed: false; manifest: HomeOutboxManifest };

/** Durable Home submissions for this profile. */
export interface HomeOutbox {
  read(): Promise<HomeOutboxManifest>;
  /** Replaces the manifest only if its stored revision still equals expected. */
  commit(next: HomeOutboxManifest, expected: number): Promise<CommitOutcome>;
  /** Publishes one attachment's bytes; the manifest names the item first. */
  putBlob(itemId: string, bytes: Blob): Promise<void>;
  readBlob(itemId: string): Promise<Blob | undefined>;
  /** Removes bytes no saved record names. */
  sweep(): Promise<void>;
  /** Size and modification time of a local file, when the platform reports them. */
  fingerprint(path: string): Promise<PathFingerprint | undefined>;
}

export class HomeOutboxFormatError extends Error {}

function validManifest(value: unknown): HomeOutboxManifest {
  if (value == null) return emptyManifest();
  const manifest = value as HomeOutboxManifest;
  if (manifest.format !== 1 || !Number.isSafeInteger(manifest.revision) || !Array.isArray(manifest.launches)) {
    throw new HomeOutboxFormatError("Saved Home ideas have an unknown format; they were left untouched.");
  }
  return manifest;
}

const nativeOutbox: HomeOutbox = {
  async read() {
    return validManifest(await invoke<unknown>("read_home_outbox"));
  },
  async commit(next, expected) {
    const outcome = await invoke<{ committed: boolean; manifest: unknown }>("commit_home_outbox", {
      manifest: next,
      expectedRevision: expected,
    });
    return { committed: outcome.committed, manifest: validManifest(outcome.manifest) } as CommitOutcome;
  },
  async putBlob(itemId, bytes) {
    await invoke("put_home_outbox_blob", new Uint8Array(await bytes.arrayBuffer()), {
      headers: { "x-home-item": itemId },
    });
  },
  async readBlob(itemId) {
    try {
      const bytes = await invoke<ArrayBuffer>("read_home_outbox_blob", { itemId });
      return new Blob([bytes]);
    } catch (error) {
      if (String(error) === "missing") return undefined;
      throw error;
    }
  },
  async sweep() {
    await invoke("sweep_home_outbox");
  },
  async fingerprint(path) {
    return (await invoke<PathFingerprint | null>("fingerprint_home_path", { path })) ?? undefined;
  },
};

const DATABASE = "painted-wolf-home-starts";
const MANIFEST_KEY = "current";
let database: Promise<IDBDatabase> | undefined;

function openDatabase(): Promise<IDBDatabase> {
  database ??= new Promise((resolve, reject) => {
    const request = indexedDB.open(DATABASE, 1);
    request.onupgradeneeded = () => {
      request.result.createObjectStore("manifest");
      request.result.createObjectStore("blobs");
    };
    request.onsuccess = () => {
      const db = request.result;
      db.onversionchange = () => { db.close(); database = undefined; };
      resolve(db);
    };
    request.onblocked = () => { database = undefined; reject(new Error("Another window is holding Home storage open.")); };
    request.onerror = () => { database = undefined; reject(request.error ?? new Error("Home storage could not be opened.")); };
  });
  return database;
}

function run<T>(mode: IDBTransactionMode, body: (tx: IDBTransaction, done: (value: T) => void) => void): Promise<T> {
  return openDatabase().then((db) => new Promise<T>((resolve, reject) => {
    const tx = db.transaction(["manifest", "blobs"], mode, { durability: "strict" });
    let result: T;
    let failure: Error | undefined;
    try {
      body(tx, (value) => { result = value; });
    } catch (error) {
      failure = error instanceof Error ? error : new Error(String(error));
      tx.abort();
    }
    tx.oncomplete = () => resolve(result);
    tx.onabort = () => reject(failure ?? tx.error ?? new Error("Home storage could not be written."));
    tx.onerror = () => reject(tx.error ?? new Error("Home storage could not be written."));
  }));
}

const browserOutbox: HomeOutbox = {
  read: () => run<unknown>("readonly", (tx, done) => {
    const request = tx.objectStore("manifest").get(MANIFEST_KEY);
    request.onsuccess = () => done(request.result);
  }).then(validManifest),
  commit: (next, expected) => run<CommitOutcome>("readwrite", (tx, done) => {
    const store = tx.objectStore("manifest");
    const request = store.get(MANIFEST_KEY);
    request.onsuccess = () => {
      const current = validManifest(request.result);
      if (current.revision !== expected) { done({ committed: false, manifest: current }); return; }
      const stored = { ...next, format: 1 as const, revision: expected + 1 };
      store.put(stored, MANIFEST_KEY);
      done({ committed: true, manifest: stored });
    };
  }),
  putBlob: (itemId, bytes) => run<void>("readwrite", (tx) => {
    tx.objectStore("blobs").put(bytes, itemId);
  }),
  readBlob: (itemId) => run<Blob | undefined>("readonly", (tx, done) => {
    const request = tx.objectStore("blobs").get(itemId);
    request.onsuccess = () => done(request.result as Blob | undefined);
  }),
  // Reachability is read in the same transaction that deletes.
  sweep: () => run<void>("readwrite", (tx) => {
    const manifest = tx.objectStore("manifest").get(MANIFEST_KEY);
    manifest.onsuccess = () => {
      const keep = reachableItemIds(validManifest(manifest.result));
      const keys = tx.objectStore("blobs").getAllKeys();
      keys.onsuccess = () => {
        for (const key of keys.result) if (!keep.has(String(key))) tx.objectStore("blobs").delete(key);
      };
    };
  }),
  fingerprint: () => Promise.resolve(undefined),
};

/** An outbox that keeps everything in memory; used where no storage exists. */
export function memoryHomeOutbox(initial: HomeOutboxManifest = emptyManifest()): HomeOutbox & {
  blobs: Map<string, Blob>;
  manifest: () => HomeOutboxManifest;
} {
  let manifest = structuredClone(initial);
  const blobs = new Map<string, Blob>();
  return {
    blobs,
    manifest: () => manifest,
    read: async () => structuredClone(manifest),
    commit: async (next, expected) => {
      if (manifest.revision !== expected) return { committed: false, manifest: structuredClone(manifest) };
      manifest = structuredClone({ ...next, format: 1, revision: expected + 1 });
      return { committed: true, manifest: structuredClone(manifest) };
    },
    putBlob: async (itemId, bytes) => { blobs.set(itemId, bytes); },
    readBlob: async (itemId) => blobs.get(itemId),
    sweep: async () => {
      const keep = reachableItemIds(manifest);
      for (const key of [...blobs.keys()]) if (!keep.has(key)) blobs.delete(key);
    },
    fingerprint: async () => undefined,
  };
}

let override: HomeOutbox | undefined;

export function homeOutbox(): HomeOutbox {
  if (override) return override;
  if (isTauriRuntime()) return nativeOutbox;
  if (typeof indexedDB !== "undefined") return browserOutbox;
  return (override = memoryHomeOutbox());
}

export function setHomeOutboxForTest(outbox: HomeOutbox | undefined): void {
  override = outbox;
}
