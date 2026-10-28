import { afterEach, describe, expect, it, vi } from "vitest";
import { SECRET_COPY_LIFETIME_MS, secretClipboardLease } from "./secret-clipboard.ts";

afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); });

function deferred<T>() {
  let resolve: (value: T) => void = () => { throw new Error("deferred promise is not initialized"); };
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("secret clipboard lease", () => {
  it("clears the owned copy only at expiry", async () => {
    vi.useFakeTimers();
    const adapter = { write: vi.fn(async () => 9), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    expect(await lease.write("private")).toBe(true);
    await vi.advanceTimersByTimeAsync(SECRET_COPY_LIFETIME_MS - 1);
    expect(adapter.clear).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(adapter.clear.mock.calls).toEqual([[9]]);
  });

  it("restarts the timer for a second copy and clears only that copy", async () => {
    vi.useFakeTimers();
    const adapter = { write: vi.fn().mockResolvedValueOnce(1).mockResolvedValueOnce(2), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    await lease.write("first");
    await vi.advanceTimersByTimeAsync(SECRET_COPY_LIFETIME_MS / 2);
    await lease.write("second");
    await vi.advanceTimersByTimeAsync(SECRET_COPY_LIFETIME_MS - 1);
    expect(adapter.clear).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(adapter.clear.mock.calls).toEqual([[2]]);
  });

  it("ignores a late completion from an earlier copy", async () => {
    vi.useFakeTimers();
    const first = deferred<number>();
    const adapter = { write: vi.fn().mockReturnValueOnce(first.promise).mockResolvedValueOnce(2), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    const old = lease.write("first");
    expect(await lease.write("second")).toBe(true);
    first.resolve(1);
    expect(await old).toBe(false);
    await vi.advanceTimersByTimeAsync(SECRET_COPY_LIFETIME_MS);
    expect(adapter.clear.mock.calls).toEqual([[2]]);
  });

  it("keeps the earlier timer when a later write fails", async () => {
    vi.useFakeTimers();
    const adapter = { write: vi.fn().mockResolvedValueOnce(1).mockRejectedValueOnce(new Error("refused")), clear: vi.fn(async () => {}) };
    const lease = secretClipboardLease(adapter);
    await lease.write("first");
    await expect(lease.write("second")).rejects.toThrow("refused");
    await vi.advanceTimersByTimeAsync(SECRET_COPY_LIFETIME_MS);
    expect(adapter.clear.mock.calls).toEqual([[1]]);
  });
});
