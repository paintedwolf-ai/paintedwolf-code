import { DocumentStorageError, DocumentRecoveryFormatError } from "./document-outbox.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import { FileDocumentOpeningController, type DocumentOpeningTarget } from "./file-document-opening.ts";

const target = { key: "file", identity: "workspace/file/1", instance: {}, value: "file" };
function setup() {
  const options = {
    open: vi.fn<(_target: string, _incarnation: string) => Promise<string | null>>().mockResolvedValue("document"),
    current: vi.fn((_target: DocumentOpeningTarget<string>) => true),
    state: vi.fn(), accept: vi.fn(), release: vi.fn(async () => {}),
  };
  const controller = new FileDocumentOpeningController<string, string>(options);
  return { ...options, controller };
}
async function settle() { for (let i = 0; i < 6; i++) await Promise.resolve(); }
afterEach(() => vi.useRealTimers());

describe("file document opening lifecycle", () => {
  it("bounds background joins and reserves capacity for a newly selected file", async () => {
    const f = setup(), connection = {};
    const finish = new Map<string, (document: string) => void>();
    f.open.mockImplementation(value => new Promise(resolve => finish.set(value, resolve)));
    const background = ["a", "b", "c"].map(key => ({ ...target, key, value: key, background: true }));
    f.controller.reconcile(connection, true, background);
    expect(f.open.mock.calls.map(call => call[0])).toEqual(["a"]);
    const selected = { ...target, key: "selected", value: "selected" };
    f.controller.reconcile(connection, true, [...background, selected]);
    expect(f.open.mock.calls.map(call => call[0])).toEqual(["a", "selected"]);
    finish.get("a")!("a-document");
    await settle();
    expect(f.open.mock.calls.map(call => call[0])).toEqual(["a", "selected", "b"]);
    f.controller.reconcile(connection, true, [selected]);
    finish.get("b")!("b-document");
    finish.get("selected")!("selected-document");
    await settle();
    expect(f.open).toHaveBeenCalledTimes(3);
    expect(f.release).toHaveBeenCalledWith("b-document");
    expect(f.accept).toHaveBeenCalledWith("selected", "selected-document");
    f.controller.dispose();
  });

  it("retries an obsolete failure after the backend connection has been replaced", async () => {
    vi.useFakeTimers();
    const f = setup();
    let fail!: (error: Error) => void;
    f.open.mockImplementationOnce(() => new Promise((_resolve, reject) => { fail = reject; }));
    f.controller.reconcile({}, true, [target]);
    f.controller.reconcile({}, true, [target]);
    fail(new LycaonApiError("Old backend failure", 500, "internal_error", { retryable: false }));
    await settle();
    expect(f.state).toHaveBeenLastCalledWith("file", expect.objectContaining({ status: "reconnecting" }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });

  it("retries a failed join explicitly without releasing its document or duplicating a running join", async () => {
    const f = setup();
    f.open.mockRejectedValueOnce(new LycaonApiError("Document storage is unavailable", 500, "internal_error"));
    f.controller.reconcile({}, true, [target]);
    await settle();
    f.controller.retry(target.key);
    f.controller.retry(target.key);
    await settle();
    expect(f.open).toHaveBeenCalledTimes(2);
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    expect(f.release).not.toHaveBeenCalled();
    f.controller.dispose();
  });

  it("leaves an unknown durable recovery format blocked without reconnect retries", async () => {
    vi.useFakeTimers();
    const f = setup();
    f.open.mockRejectedValue(new DocumentStorageError(new DocumentRecoveryFormatError("Unknown recovery format")));
    f.controller.reconcile({}, true, [target]);
    await settle();
    expect(f.state).toHaveBeenLastCalledWith("file", { status: "error", editingBlock: "storage", message: "Unknown recovery format" });
    await vi.advanceTimersByTimeAsync(120_000);
    expect(f.open).toHaveBeenCalledOnce();
    f.controller.dispose();
  });

  it("deduplicates an opening file and accepts its document automatically", async () => {
    const f = setup(), connection = {};
    f.controller.reconcile(connection, true, [target]);
    f.controller.reconcile(connection, true, [target]);
    await settle();
    expect(f.open).toHaveBeenCalledOnce();
    expect(f.state).toHaveBeenCalledWith("file", { status: "opening" });
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });

  it("waits for connectivity and opens without a person clicking retry", async () => {
    const f = setup(), connection = {};
    f.controller.reconcile(connection, false, [target]);
    expect(f.open).not.toHaveBeenCalled();
    expect(f.state).toHaveBeenLastCalledWith("file", expect.objectContaining({ status: "reconnecting" }));
    f.controller.reconcile(connection, true, [target]);
    await settle();
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });

  it("backs off transient failures and keeps the error local to the file", async () => {
    vi.useFakeTimers();
    const f = setup(), connection = {};
    f.open.mockRejectedValueOnce(new BackendTransportError(new Error("offline"), "unreachable"))
      .mockRejectedValueOnce(new LycaonApiError("Server busy", 429, "rate_limited"));
    f.controller.reconcile(connection, true, [target]);
    await settle();
    expect(f.state).toHaveBeenLastCalledWith("file", expect.objectContaining({ status: "reconnecting" }));
    await vi.advanceTimersByTimeAsync(999);
    expect(f.open).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(f.open).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1999);
    expect(f.open).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    expect(new Set(f.open.mock.calls.map((call) => call[1])).size).toBe(1);
    f.controller.dispose();
  });

  it("retries native preservation failures during opening without a maintenance action", async () => {
    vi.useFakeTimers();
    const f = setup();
    f.open.mockRejectedValueOnce(new DocumentStorageError("Storage temporarily unavailable"));
    f.controller.reconcile({}, true, [target]);
    await settle();
    expect(f.state).toHaveBeenLastCalledWith("file", expect.objectContaining({ status: "reconnecting" }));
    await vi.advanceTimersByTimeAsync(1000);
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });

  it("surfaces permanent failure without polling and retries after the source changes", async () => {
    vi.useFakeTimers();
    const f = setup(), connection = {};
    f.open.mockRejectedValueOnce(new LycaonApiError("Invalid checkpoint", 409, "editor_replica_epoch", { retryable: false }));
    f.controller.reconcile(connection, true, [target]);
    await settle();
    f.controller.reconcile(connection, true, [target]);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(f.open).toHaveBeenCalledOnce();
    expect(f.state).toHaveBeenLastCalledWith("file", { status: "error", message: "Invalid checkpoint" });
    f.controller.reconcile(connection, true, [{ ...target, identity: "new-source" }]);
    await settle();
    expect(f.accept).toHaveBeenCalledOnce();
    f.controller.dispose();
  });

  it.each(["closed", "reopened", "workspace", "disposed"])("retires a late response after its target is %s", async (reason) => {
    const f = setup(), connection = {};
    let finish!: (document: string) => void;
    f.open.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    f.controller.reconcile(connection, true, [target]);
    if (reason === "disposed") f.controller.dispose();
    else f.controller.reconcile(connection, true, reason === "closed" ? [] : [reason === "reopened" ? { ...target, instance: {} } : { ...target, identity: "another-workspace" }]);
    finish("old-document");
    await settle();
    expect(f.accept).not.toHaveBeenCalledWith("file", "old-document");
    expect(f.release).toHaveBeenCalledWith("old-document");
    f.controller.dispose();
  });

  it("does not retire a valid in-flight response just because reachability changed", async () => {
    const f = setup(), connection = {};
    let finish!: (document: string) => void;
    f.open.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    f.controller.reconcile(connection, true, [target]);
    f.controller.reconcile(connection, false, [target]);
    f.controller.reconcile(connection, true, [target]);
    finish("document");
    await settle();
    expect(f.open).toHaveBeenCalledOnce();
    expect(f.release).not.toHaveBeenCalled();
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });

  it("cancels scheduled retries when the file closes", async () => {
    vi.useFakeTimers();
    const f = setup(), connection = {};
    f.open.mockRejectedValue(new LycaonApiError("Server busy", 429, "rate_limited"));
    f.controller.reconcile(connection, true, [target]);
    await settle();
    f.controller.reconcile(connection, true, []);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(f.open).toHaveBeenCalledOnce();
    f.controller.dispose();
  });

  it("recovers automatically when the opening request times out", async () => {
    vi.useFakeTimers();
    const f = setup();
    f.open.mockRejectedValueOnce(new DOMException("Opening timed out", "TimeoutError"));
    f.controller.reconcile({}, true, [target]);
    await settle();
    expect(f.state).toHaveBeenLastCalledWith("file", { status: "reconnecting", message: "Opening timed out" });
    await vi.advanceTimersByTimeAsync(1000);
    expect(f.accept).toHaveBeenCalledWith("file", "document");
    f.controller.dispose();
  });
});
