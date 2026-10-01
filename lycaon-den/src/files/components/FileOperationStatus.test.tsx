import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceOperationStatus } from "../../api/types.ts";
import { applySourceOperationEvent, resetSourceOperationsForTests, resyncSourceOperations } from "../../store/source-operations.ts";
import { FileOperationStatus } from "./FileOperationStatus.tsx";

const pending: SourceOperationStatus = {
  operation_id: "operation", root_id: "root", path: "large", from_path: "", state: "running", phase: "preserving", complete: false, operation: "deleteProjectSource", cancelable: true, entries_processed: 14000, bytes_processed: 200000000, created_at: "2026-09-14T00:00:00Z",
};
const event = (operation: SourceOperationStatus) => applySourceOperationEvent({ project_id: "project", operation });
afterEach(() => { cleanup(); resetSourceOperationsForTests(); });

it("discovers pending work and cancels by its durable identity", async () => {
  const cancel = vi.fn(async () => undefined);
  const client = stubClient({ listSourceOperations: vi.fn(async () => ({ operations: [pending] })), cancelSourceOperation: cancel });
  render(() => <FileOperationStatus projectId="project" client={client} onRetry={async () => {}} onComplete={() => {}} />);
  await screen.findByText(/14,000 entries · 190.7 MiB processed/);
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(cancel).toHaveBeenCalledWith("project", "operation"));
});

it("lists once per event stream and follows progress from events", async () => {
  const listSourceOperations = vi.fn(async () => ({ operations: [pending] }));
  render(() => <FileOperationStatus projectId="project" client={stubClient({ listSourceOperations })} onRetry={async () => {}} onComplete={() => {}} />);
  await screen.findByText(/14,000 entries/);
  event({ ...pending, entries_processed: 20000 });
  await screen.findByText(/20,000 entries/);
  expect(listSourceOperations).toHaveBeenCalledTimes(1);
  // A fresh stream may have missed transitions, so the list is read again.
  resyncSourceOperations();
  await waitFor(() => expect(listSourceOperations).toHaveBeenCalledTimes(2));
});

it("keeps a completed event when an older in-flight list arrives", async () => {
  let resolveList!: (value: { operations: SourceOperationStatus[] }) => void;
  const listSourceOperations = vi.fn(() => new Promise<{ operations: SourceOperationStatus[] }>((resolve) => { resolveList = resolve; }));
  const onComplete = vi.fn();
  const client = stubClient({ listSourceOperations });
  render(() => <FileOperationStatus projectId="project" client={client} onRetry={async () => {}} onComplete={onComplete} />);
  await waitFor(() => expect(listSourceOperations).toHaveBeenCalledOnce());
  event(pending);
  await screen.findByText(/14,000 entries/);
  const completed: SourceOperationStatus = { ...pending, state: "completed", complete: true, cancelable: false, completed_at: new Date().toISOString() };
  event(completed);
  await screen.findByText("Completed");
  resolveList({ operations: [pending] });
  await waitFor(() => expect(onComplete).toHaveBeenCalledExactlyOnceWith(completed));
  await Promise.resolve();
  expect(screen.getByText("Completed")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
});

it("shows terminal failure without a spinner and offers an explicit retry", async () => {
  const retry = vi.fn(async () => undefined);
  const failed: SourceOperationStatus = { ...pending, state: "failed", complete: true, cancelable: false, error: { code: "internal_error", message: "Disk is full", retryable: true } };
  const client = stubClient({ listSourceOperations: vi.fn(async () => ({ operations: [failed] })) });
  render(() => <FileOperationStatus projectId="project" client={client} onRetry={retry} onComplete={() => {}} />);
  await screen.findByText("Disk is full");
  expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(retry).toHaveBeenCalledWith(failed));
  fireEvent.click(screen.getByRole("button", { name: /Dismiss/ }));
  expect(screen.queryByTestId("file-operations")).toBeNull();
});

it("keeps canceled operations available for retry until dismissed", async () => {
  const canceled: SourceOperationStatus = { ...pending, state: "canceled", complete: true, cancelable: false };
  const retry = vi.fn(async () => undefined);
  const client = stubClient({ listSourceOperations: vi.fn(async () => ({ operations: [canceled] })) });
  render(() => <FileOperationStatus projectId="project" client={client} onRetry={retry} onComplete={() => {}} />);
  await screen.findByText("Canceled");
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(retry).toHaveBeenCalledWith(canceled));
});

it("reports each completion the events deliver, never one the list already held", async () => {
  const older: SourceOperationStatus = { ...pending, operation_id: "older", operation: "undoProjectSourceHistory" };
  const newer: SourceOperationStatus = { ...pending, operation_id: "newer", operation: "redoProjectSourceHistory" };
  const done: SourceOperationStatus = { ...pending, operation_id: "done", state: "completed", complete: true, cancelable: false, completed_at: new Date().toISOString() };
  const listSourceOperations = vi.fn(async () => ({ operations: [newer, older, done] }));
  const onComplete = vi.fn();
  render(() => <FileOperationStatus projectId="project" client={stubClient({ listSourceOperations })} onRetry={async () => {}} onComplete={onComplete} />);
  await screen.findByTestId("file-operations");
  // A completion the list already held is not a transition.
  expect(onComplete).not.toHaveBeenCalled();
  const completed: SourceOperationStatus[] = [newer, older].map((entry) => ({ ...entry, state: "completed", complete: true, cancelable: false, completed_at: new Date().toISOString() }));
  for (const entry of completed) event(entry);
  await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(2));
  expect(onComplete.mock.calls.map(([entry]) => (entry as SourceOperationStatus).operation_id)).toEqual(["newer", "older"]);
});

it("allows cancellation while a retried operation is still running", async () => {
  let finishRetry: (() => void) | undefined;
  const retry = vi.fn(() => {
    event(pending);
    return new Promise<void>((resolve) => { finishRetry = resolve; });
  });
  const cancelSourceOperation = vi.fn(async () => { finishRetry?.(); });
  const client = stubClient({
    listSourceOperations: vi.fn(async () => ({ operations: [{ ...pending, state: "canceled", complete: true, cancelable: false }] })),
    cancelSourceOperation,
  });
  render(() => <FileOperationStatus projectId="project" client={client} onRetry={retry} onComplete={() => {}} />);
  fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Cancel" }).hasAttribute("disabled")).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await waitFor(() => expect(cancelSourceOperation).toHaveBeenCalledWith("project", "operation"));
});
