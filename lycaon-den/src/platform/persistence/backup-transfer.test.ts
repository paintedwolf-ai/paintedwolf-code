import { beforeEach, describe, expect, it, vi } from "vitest";
import { transferNativeBackup, backupProgressMessage, type BackupTransferProgress } from "./backup-transfer.ts";

const { invoke, listen, unlisten } = vi.hoisted(() => ({ invoke: vi.fn(), listen: vi.fn(), unlisten: vi.fn() }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("@tauri-apps/api/event", () => ({ listen }));

describe("native backup transfer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listen.mockResolvedValue(unlisten);
    invoke.mockResolvedValue({ saved: true });
  });

  it("passes only a picker grant and operation identity across IPC", async () => {
    await expect(transferNativeBackup("one-use-grant", false)).resolves.toEqual({ saved: true });
    expect(invoke).toHaveBeenCalledWith("transfer_backup", { grant: "one-use-grant", restore: false, id: expect.any(String) });
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("routes only this transfer's progress and cleans up after cancellation", async () => {
    let finish!: (value: { cancelled: true }) => void;
    invoke.mockImplementation((command: string) => command === "transfer_backup"
      ? new Promise((resolve) => { finish = resolve; }) : Promise.resolve(true));
    const controller = new AbortController();
    const onProgress = vi.fn();
    const result = transferNativeBackup("grant", true, { signal: controller.signal, onProgress });
    await vi.waitFor(() => expect(invoke).toHaveBeenCalled());
    const id = invoke.mock.calls[0]![1].id;
    const emit = listen.mock.calls[0]![1];
    const progress: BackupTransferProgress = { id, phase: "uploading", bytes: 1024, total: 2048, cancellable: true };
    emit({ payload: { ...progress, id: "another-transfer" } });
    expect(onProgress).not.toHaveBeenCalled();
    emit({ payload: progress });
    expect(onProgress).toHaveBeenCalledWith(progress);
    controller.abort();
    expect(invoke).toHaveBeenCalledWith("cancel_backup_transfer", { id });
    finish({ cancelled: true });
    await expect(result).resolves.toEqual({ cancelled: true });
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("retries cancellation when native registration follows the abort", async () => {
    let finish!: (value: { cancelled: true }) => void;
    invoke.mockImplementation((command: string) => command === "transfer_backup"
      ? new Promise((resolve) => { finish = resolve; }) : Promise.resolve(false));
    const controller = new AbortController();
    const result = transferNativeBackup("grant", false, { signal: controller.signal });
    await vi.waitFor(() => expect(invoke).toHaveBeenCalledWith("transfer_backup", expect.anything()));
    const id = invoke.mock.calls[0]![1].id;
    controller.abort();
    expect(invoke.mock.calls.filter(([command]) => command === "cancel_backup_transfer")).toHaveLength(1);
    const emit = listen.mock.calls[0]![1];
    emit({ payload: { id: "other", phase: "preparing", bytes: 0, total: null, cancellable: true } });
    expect(invoke.mock.calls.filter(([command]) => command === "cancel_backup_transfer")).toHaveLength(1);
    emit({ payload: { id, phase: "preparing", bytes: 0, total: null, cancellable: true } });
    expect(invoke.mock.calls.filter(([command]) => command === "cancel_backup_transfer")).toHaveLength(2);
    emit({ payload: { id, phase: "saving", bytes: 100, total: 100, cancellable: false } });
    expect(invoke.mock.calls.filter(([command]) => command === "cancel_backup_transfer")).toHaveLength(2);
    finish({ cancelled: true });
    await expect(result).resolves.toEqual({ cancelled: true });
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("does not start an already cancelled transfer", async () => {
    await expect(transferNativeBackup("grant", false, { signal: AbortSignal.abort() })).resolves.toEqual({ cancelled: true });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("does not start when cancellation arrives while connecting progress", async () => {
    let subscribed!: (stop: () => void) => void;
    listen.mockImplementation(() => new Promise((resolve) => { subscribed = resolve; }));
    const controller = new AbortController();
    const result = transferNativeBackup("grant", false, { signal: controller.signal });
    await vi.waitFor(() => expect(listen).toHaveBeenCalled());
    controller.abort();
    subscribed(unlisten);
    await expect(result).resolves.toEqual({ cancelled: true });
    expect(invoke).not.toHaveBeenCalled();
    expect(unlisten).toHaveBeenCalledOnce();
  });

  it("reports preparation and validation without invented percentages", () => {
    expect(backupProgressMessage({ id: "a", phase: "preparing", bytes: 0, total: null, cancellable: true })).toBe("Preparing backup…");
    expect(backupProgressMessage({ id: "a", phase: "validating", bytes: 100, total: 100, cancellable: false })).toBe("Checking and staging restore…");
  });
});
