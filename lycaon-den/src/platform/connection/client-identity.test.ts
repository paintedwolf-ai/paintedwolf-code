// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

/** A fresh module instance is what a reloaded document starts with. */
async function loadDocument(): Promise<() => string> {
  vi.resetModules();
  const { clientIdentity } = await import("./client-identity.ts");
  return clientIdentity;
}

describe("client identity", () => {
  beforeEach(() => sessionStorage.clear());
  afterEach(() => vi.unstubAllGlobals());

  it("answers with one identity for the life of the document", async () => {
    const clientIdentity = await loadDocument();
    expect(clientIdentity()).toBe(clientIdentity());
  });

  // Reloads use the same preserved update outbox.
  it("keeps its identity across a reload of the same window", async () => {
    const before = (await loadDocument())();
    expect((await loadDocument())()).toBe(before);
  });

  it("never hands one window the identity of another", async () => {
    const other = (await loadDocument())();
    sessionStorage.clear();
    expect((await loadDocument())()).not.toBe(other);
  });
  it("claims a new identity when a duplicated tab inherits a live window's storage", async () => {
    const held = new Set<string>();
    vi.stubGlobal("navigator", { locks: {
      request: async (name: string, _options: unknown, callback: (lock: object | null) => Promise<void>) => {
        if (held.has(name)) return callback(null);
        held.add(name);
        await callback({ name, mode: "exclusive" });
        held.delete(name);
      },
    } });
    vi.resetModules();
    const first = await import("./client-identity.ts");
    const original = await first.prepareClientIdentity();
    vi.resetModules();
    const second = await import("./client-identity.ts");
    expect(second.clientIdentity()).toBe(original);
    expect(await second.prepareClientIdentity()).not.toBe(original);
    expect(first.clientIdentity()).toBe(original);
  });

});
