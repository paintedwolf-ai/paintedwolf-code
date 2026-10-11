import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { LycaonApiError, lycaonFetch, lycaonJson } from "./http.ts";
import { sourceOperation, sourceOperationResult, SourceOperationStoppedError } from "./source-operation.ts";
import { deliverSourceOperationCompletion } from "./source-operation-completion.ts";
import type { SourceOperationStatus } from "./types.ts";

vi.mock("./http.ts", async (original) => ({
  ...await original<typeof import("./http.ts")>(), lycaonFetch: vi.fn(), lycaonJson: vi.fn(),
}));
const connection = { baseUrl: "http://localhost", apiToken: "token" };
const id = "040e18eb-04af-4464-af53-e66002e42c58";
const path = `/v1/projects/project/source?path=large&operation_id=${id}`;
function status(change: Partial<SourceOperationStatus> = {}): SourceOperationStatus {
  return { operation_id: id, root_id: "root", path: "large", from_path: "", complete: false, state: "running", phase: "preserving", operation: "deleteProjectSource", cancelable: true, entries_processed: 14000, bytes_processed: 200000000, created_at: "2026-09-14T00:00:00Z", ...change };
}
beforeEach(() => { vi.useFakeTimers(); vi.resetAllMocks(); });
afterEach(() => vi.useRealTimers());

describe("durable source operations", () => {
  it("reconnects by the original operation identity without resubmitting", async () => {
    vi.mocked(lycaonFetch).mockRejectedValueOnce(new BackendTransportError(new TypeError("disconnected"), "reachable"));
    vi.mocked(lycaonJson).mockResolvedValueOnce(status()).mockResolvedValueOnce(status({ state: "completed", complete: true, cancelable: false }));
    const result = sourceOperation(connection, "project", id, path, { method: "DELETE" });
    await vi.advanceTimersByTimeAsync(1600);
    await expect(result).resolves.toBeUndefined();
    expect(lycaonFetch).toHaveBeenCalledOnce();
    expect(lycaonJson).toHaveBeenCalledWith(connection, `/v1/projects/project/source/operations/${id}`, { signal: expect.any(AbortSignal) });
  });

  it("preserves the host error and recovery guidance after acceptance", async () => {
    vi.mocked(lycaonFetch).mockResolvedValueOnce(Response.json(status(), { status: 202 }));
    vi.mocked(lycaonJson).mockResolvedValueOnce(status({ complete: true, state: "failed", error: { code: "source_recovery_failed", message: "Disk is full", retryable: true, suggested_action: "Free disk space and retry." } }));
    const result = sourceOperation(connection, "project", id, path, { method: "DELETE" }).catch((error: unknown) => error);
    await vi.advanceTimersByTimeAsync(800);
    expect(await result).toBeInstanceOf(LycaonApiError);
    expect(await result).toMatchObject({ status: undefined, message: "Disk is full", retryable: true, suggestedAction: "Free disk space and retry." });
  });

  it("reconciles a response body that was lost after headers arrived", async () => {
    const response = new Response(null, { status: 200 });
    vi.spyOn(response, "json").mockRejectedValueOnce(new TypeError("body interrupted"));
    vi.mocked(lycaonFetch).mockResolvedValueOnce(response);
    vi.mocked(lycaonJson).mockResolvedValueOnce(status({ complete: true, state: "completed", result: { path: "large" } }));
    const result = sourceOperation(connection, "project", id, path, { method: "DELETE" });
    await vi.advanceTimersByTimeAsync(800);
    await expect(result).resolves.toEqual({ path: "large" });
    expect(lycaonFetch).toHaveBeenCalledOnce();
  });

  it.each(["canceled", "interrupted"] as const)("stops on a %s receipt even without an HTTP result", async (state) => {
    await expect(sourceOperationResult(status({ complete: true, state }))).rejects.toBeInstanceOf(SourceOperationStoppedError);
  });
});

it("settles from an authenticated completion before HTTP admission returns", async () => {
  vi.mocked(lycaonFetch).mockReturnValueOnce(new Promise(() => {}));
  const result = sourceOperation(connection, "project", id, path, { method: "DELETE" });
  deliverSourceOperationCompletion(connection, { project_id: "project", operation: status({ complete: true, state: "completed", result: { path: "moved" } }) });
  await expect(result).resolves.toEqual({ path: "moved" });
  expect(lycaonJson).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

it("delivers completion without advancing the polling clock and releases its timer", async () => {
  vi.mocked(lycaonFetch).mockResolvedValueOnce(Response.json(status(), { status: 202 }));
  const result = sourceOperation(connection, "project", id, path, { method: "DELETE" });
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  deliverSourceOperationCompletion(connection, { project_id: "project", operation: status({ complete: true, state: "completed" }) });
  await expect(result).resolves.toBeUndefined();
  expect(lycaonJson).not.toHaveBeenCalled();
  expect(vi.getTimerCount()).toBe(0);
});

it("refuses completion from another host, project, request, or engine generation", async () => {
  vi.mocked(lycaonFetch).mockResolvedValueOnce(Response.json(status(), { status: 202 }));
  const result = sourceOperation(connection, "project", id, path, { method: "DELETE" });
  const event = { project_id: "project", operation: status({ complete: true, state: "completed" }) };
  for (const other of [{ ...connection, baseUrl: "http://elsewhere" }, { ...connection, apiToken: "other" }, { ...connection, engineGeneration: 2 }]) {
    deliverSourceOperationCompletion(other, event);
  }
  deliverSourceOperationCompletion(connection, { ...event, project_id: "other" });
  deliverSourceOperationCompletion(connection, { ...event, operation: { ...event.operation, operation_id: "other" } });
  deliverSourceOperationCompletion(connection, { ...event, operation: status() });
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  deliverSourceOperationCompletion(connection, event);
  await expect(result).resolves.toBeUndefined();
  expect(vi.getTimerCount()).toBe(0);
});


it("releases the completion follower when its caller stops waiting", async () => {
  vi.mocked(lycaonFetch).mockResolvedValueOnce(Response.json(status(), { status: 202 }));
  const controller = new AbortController();
  const result = sourceOperation(connection, "project", id, path, { method: "DELETE", signal: controller.signal });
  const rejected = expect(result).rejects.toMatchObject({ name: "AbortError" });
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  controller.abort();
  await rejected;
  expect(vi.getTimerCount()).toBe(0);
  deliverSourceOperationCompletion(connection, { project_id: "project", operation: status({ complete: true, state: "completed" }) });
  await vi.advanceTimersByTimeAsync(1600);
  expect(lycaonJson).not.toHaveBeenCalled();
});


it.each([new Error("Caller stopped waiting"), "caller stopped waiting"])("stops receipt polling with an Error for abort reason %s", async (reason) => {
  vi.mocked(lycaonFetch).mockResolvedValueOnce(Response.json(status(), { status: 202 }));
  const controller = new AbortController();
  const result = sourceOperation(connection, "project", id, path, { method: "DELETE", signal: controller.signal });
  const rejected = reason instanceof Error ? expect(result).rejects.toBe(reason) : expect(result).rejects.toMatchObject({ name: "AbortError" });
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  controller.abort(reason);
  await rejected;
  expect(vi.getTimerCount()).toBe(0);
  await vi.advanceTimersByTimeAsync(1600);
  expect(lycaonFetch).toHaveBeenCalledOnce();
  expect(lycaonJson).not.toHaveBeenCalled();
});
