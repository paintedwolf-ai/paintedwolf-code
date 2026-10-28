import { describe, expect, it, vi } from "vitest";
import { secretClipboardLease } from "./secret-clipboard.ts";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("secret clipboard ownership", () => {
  it("clears the owned receipt without reading clipboard bytes", async () => {
    const adapter = { write: vi.fn(async () => 17), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    expect(await lease.write("private")).toBe(true);
    lease.clear();
    lease.clear();
    expect(adapter.clear.mock.calls).toEqual([[17]]);
  });

  it("clears a native write that finishes after remasking", async () => {
    const pending = deferred<number>();
    const adapter = { write: vi.fn(() => pending.promise), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    const writing = lease.write("private");
    lease.clear();
    pending.resolve(23);
    expect(await writing).toBe(false);
    expect(adapter.clear).toHaveBeenCalledWith(23);
  });

  it("keeps a newer copy when an earlier completion arrives late", async () => {
    const first = deferred<number>();
    const adapter = { write: vi.fn().mockReturnValueOnce(first.promise).mockResolvedValueOnce(2), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    const old = lease.write("first");
    expect(await lease.write("second")).toBe(true);
    first.resolve(1);
    expect(await old).toBe(false);
    expect(adapter.clear.mock.calls).toEqual([[1]]);
    lease.clear();
    expect(adapter.clear.mock.calls).toEqual([[1], [2]]);
  });

  it("retains ownership when a later write fails", async () => {
    const adapter = { write: vi.fn().mockResolvedValueOnce(1).mockRejectedValueOnce(new Error("refused")), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    await lease.write("first");
    await expect(lease.write("second")).rejects.toThrow("refused");
    lease.clear();
    expect(adapter.clear).toHaveBeenCalledWith(1);
  });
});
