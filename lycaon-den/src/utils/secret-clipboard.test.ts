import { afterEach, describe, expect, it, vi } from "vitest";

const isTauriRuntime = vi.fn(() => false);
const invoke = vi.fn();

vi.mock("../platform/runtime.ts", () => ({
  isTauriRuntime: () => isTauriRuntime(),
}));

vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...args),
}));

import {
  SECRET_COPY_LIFETIME_MS,
  clearSecretClipboardText,
  secretClipboardLease,
  writeSecretClipboardText,
} from "./secret-clipboard.ts";

afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); });

function deferred<T>() {
  let resolve!: (value: T) => void;
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

describe("secret clipboard commands", () => {
  afterEach(() => {
    isTauriRuntime.mockReturnValue(false);
    invoke.mockReset();
  });

  it("writes and clears through the host commands in the desktop shell", async () => {
    isTauriRuntime.mockReturnValue(true);
    invoke.mockResolvedValueOnce(7).mockResolvedValueOnce(undefined);
    await expect(writeSecretClipboardText("private")).resolves.toBe(7);
    await clearSecretClipboardText(7);
    expect(invoke.mock.calls).toEqual([
      ["write_secret_text", { text: "private" }],
      ["clear_secret_text", { copy: 7 }],
    ]);
  });

  it("refuses to write and skips clearing outside the desktop shell", async () => {
    await expect(writeSecretClipboardText("private")).rejects.toThrow("Secret clipboard is unavailable");
    await expect(clearSecretClipboardText(1)).resolves.toBeUndefined();
    expect(invoke).not.toHaveBeenCalled();
  });
});
