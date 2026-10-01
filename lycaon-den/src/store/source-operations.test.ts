import { afterEach, describe, expect, it } from "vitest";
import type { SourceOperationStatus } from "../api/types.ts";
import {
  applySourceOperationEvent,
  replaceSourceOperations,
  resetSourceOperationsForTests,
  resyncSourceOperations,
  sourceOperationsFor,
  sourceOperationsEventRevision,
  sourceOperationsResyncRevision,
} from "./source-operations.ts";

function request(id: string, overrides: Partial<SourceOperationStatus> = {}): SourceOperationStatus {
  return {
    operation_id: id, root_id: "root", path: id, from_path: "", state: "running", phase: "preserving", complete: false,
    operation: "deleteProjectSource", cancelable: true, entries_processed: 0, bytes_processed: 0,
    created_at: "2026-09-17T00:00:00Z", ...overrides,
  };
}

afterEach(resetSourceOperationsForTests);

describe("source operation store", () => {
  it("keeps events received during a snapshot read, including newly created operations", () => {
    replaceSourceOperations("p1", [request("running"), request("retired")]);
    const revision = sourceOperationsEventRevision();
    const completed = request("running", { state: "completed", complete: true, completed_at: "2026-09-17T00:00:05Z" });
    const created = request("new", { created_at: "2026-09-17T00:00:03Z" });
    applySourceOperationEvent({ project_id: "p1", operation: completed });
    applySourceOperationEvent({ project_id: "p1", operation: created });
    replaceSourceOperations("p1", [request("running"), request("snapshot-only")], revision);
    expect(sourceOperationsFor("p1")).toEqual([created, request("snapshot-only"), completed]);
  });

  it("lets a fresh snapshot replace states from events that preceded its read", () => {
    applySourceOperationEvent({ project_id: "p1", operation: request("op", { entries_processed: 10 }) });
    const revision = sourceOperationsEventRevision();
    const updated = request("op", { entries_processed: 20 });
    replaceSourceOperations("p1", [updated], revision);
    expect(sourceOperationsFor("p1")).toEqual([updated]);
    replaceSourceOperations("p1", [], sourceOperationsEventRevision());
    expect(sourceOperationsFor("p1")).toEqual([]);
  });
  it("orders active work by submission, then results by completion, newest first, as the host lists them", () => {
    replaceSourceOperations("p1", [
      request("done-early", { state: "completed", complete: true, cancelable: false, created_at: "2026-09-17T00:00:08Z", completed_at: "2026-09-17T00:00:09Z" }),
      request("done-late", { state: "failed", complete: true, cancelable: false, created_at: "2026-09-17T00:00:00Z", completed_at: "2026-09-17T00:00:30Z" }),
      request("older", { created_at: "2026-09-17T00:00:01Z" }),
      request("newer", { created_at: "2026-09-17T00:00:05Z" }),
    ]);
    expect(sourceOperationsFor("p1").map((entry) => entry.operation_id)).toEqual(["newer", "older", "done-late", "done-early"]);
    expect(sourceOperationsFor("other")).toEqual([]);
  });

  it("holds a running request in place while its progress changes", () => {
    replaceSourceOperations("p1", [request("first", { created_at: "2026-09-17T00:00:01Z" }), request("second", { created_at: "2026-09-17T00:00:02Z" })]);
    applySourceOperationEvent({ project_id: "p1", operation: request("first", { created_at: "2026-09-17T00:00:01Z", entries_processed: 900 }) });
    expect(sourceOperationsFor("p1").map((entry) => entry.operation_id)).toEqual(["second", "first"]);
  });

  it("applies an event as the request's current state", () => {
    replaceSourceOperations("p1", [request("op", { entries_processed: 10 })]);
    applySourceOperationEvent({ project_id: "p1", operation: request("op", { entries_processed: 2_000 }) });
    applySourceOperationEvent({ project_id: "p1", operation: request("late", { created_at: "2026-09-17T00:00:03Z" }) });
    expect(sourceOperationsFor("p1").map((entry) => [entry.operation_id, entry.entries_processed])).toEqual([["late", 0], ["op", 2_000]]);
    applySourceOperationEvent({ project_id: "p1", operation: request("op", { state: "completed", complete: true, cancelable: false, completed_at: "2026-09-17T00:00:04Z" }) });
    expect(sourceOperationsFor("p1").map((entry) => entry.operation_id)).toEqual(["late", "op"]);
    expect(sourceOperationsFor("p1")[1]?.complete).toBe(true);
  });

  it("keeps the host's list bound", () => {
    replaceSourceOperations("p1", []);
    for (let n = 0; n < 120; n += 1) {
      applySourceOperationEvent({ project_id: "p1", operation: request(`op-${n}`, { created_at: `2026-09-17T00:${String(Math.floor(n / 60)).padStart(2, "0")}:${String(n % 60).padStart(2, "0")}Z` }) });
    }
    expect(sourceOperationsFor("p1")).toHaveLength(100);
    expect(sourceOperationsFor("p1")[0]?.operation_id).toBe("op-119");
  });

  it("marks a resync for consumers that re-list", () => {
    const before = sourceOperationsResyncRevision();
    resyncSourceOperations();
    expect(sourceOperationsResyncRevision()).toBe(before + 1);
  });
});
