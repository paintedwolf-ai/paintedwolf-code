import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, assert, describe, expect, it, vi } from "vitest";
import type { HistoryRetentionPolicy, HistoryRetentionRequest, HistoryStorageStatus } from "../../../api/types.ts";
import { HistoryStorageSettingsPanel } from "./HistoryStorageSettingsPanel.tsx";
import { stubClient } from "../../../test/client-fixture.ts";
import { HISTORY_CLASSES } from "../../../settings/storage/history-storage-model.ts";

const confirm = vi.fn(async (..._args: unknown[]) => true);
vi.mock("../../../platform/interaction/confirm-dialog.ts", () => ({ confirmDestructive: (...args: unknown[]) => confirm(...args) }));

function clientFixture(over: Partial<HistoryRetentionPolicy> = {}) {
  let policy: HistoryRetentionPolicy = {
    version: 1, revision: 1, suspended: false,
    recordings: { mode: "forever" }, checkpoints: { mode: "forever" },
    source_revisions: { mode: "forever" }, scan_detail: { mode: "forever" }, receipt_detail: { mode: "forever" }, ...over,
  };
  const getHistoryStorage = vi.fn(async (): Promise<HistoryStorageStatus> => ({ policy, lanes: [], protections: [] }));
  return stubClient({
    getHistoryStorage,
    listProjects: vi.fn(async () => [{ id: "project-1", name: "Example project" }]),
    listProjectSessions: vi.fn(async () => ({ sessions: [{ id: "task-1", title: "Old task" }], total: 1 })),
    previewHistoryRetention: vi.fn(async () => ({ token: "review-1", policy_revision: 1, candidates: [], eligible_count: 2, reclaimable_bytes: 2048, shared_protected_bytes: 512, complete: false })),
    updateHistoryStorage: vi.fn(async (request: HistoryRetentionRequest) => { policy = { ...policy, ...request.policy, version: 1, revision: 2, suspended: false }; return policy; }),
    pruneHistory: vi.fn().mockResolvedValueOnce({ removed_count: 1, released_bytes: 1024, complete: false, preview_token: "review-2" }).mockResolvedValueOnce({ removed_count: 1, released_bytes: 1024, complete: true, preview_token: "review-3" }),
    createHistoryProtection: vi.fn(async () => undefined),
    deleteHistoryProtection: vi.fn(async () => undefined),
  });
}
async function preview() {
  await waitFor(() => expect(screen.getByTestId("history-preview")).toBeTruthy());
  fireEvent.click(screen.getByTestId("history-preview"));
  await waitFor(() => expect(screen.getByTestId("history-save")).toBeTruthy());
}

describe("HistoryStorageSettingsPanel", () => {
  afterEach(() => { cleanup(); confirm.mockClear(); });
  it.each(HISTORY_CLASSES.flatMap((row) => ["save", "prune"].map((action) => ({ ...row, action }))))("separates preserved data from $id losses when choosing $action", async ({ id, loss, action }) => {
    const client = clientFixture({ [id]: { mode: "max_age", max_age_days: 30 } });
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await preview();
    confirm.mockResolvedValueOnce(false);
    fireEvent.click(screen.getByTestId(`history-${action}`));
    await waitFor(() => expect(confirm).toHaveBeenCalledOnce());
    const message = (confirm.mock.calls[0]![0] as { message: string }).message;
    expect(message).toContain(`Lost capabilities: ${loss}.`);
    expect(message).toContain("usage and cost totals, settings, and credentials remain.");
    expect(message).not.toContain("remain will no longer");
    for (const other of HISTORY_CLASSES.filter((row) => row.id !== id)) expect(message).not.toContain(other.loss);
    expect(client.pruneHistory).not.toHaveBeenCalled();
    expect(client.updateHistoryStorage).not.toHaveBeenCalled();
  });
  it("keeps unmeasured usage unknown and requires a review even when keeping forever", async () => {
    const client = clientFixture();
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await waitFor(() => expect(screen.getByText("Storage measurements are not yet available.")).toBeTruthy());
    expect(screen.queryByTestId("history-save")).toBeNull();
    expect(client.updateHistoryStorage).not.toHaveBeenCalled();
    await preview();
    fireEvent.click(screen.getByTestId("history-save"));
    await waitFor(() => expect(client.updateHistoryStorage).toHaveBeenCalledWith(expect.objectContaining({ preview_token: "review-1" })));
    expect(confirm).not.toHaveBeenCalled();
  });
  it("discards a preview after changing a rule and preserves the focused numeric input", async () => {
    const client = clientFixture({ recordings: { mode: "max_age", max_age_days: 30 } });
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await preview();
    const input = screen.getByLabelText("Recordings limit");
    input.focus();
    fireEvent.input(input, { target: { value: "60" } });
    expect(screen.queryByTestId("history-save")).toBeNull();
    expect(document.activeElement).toBe(input);
    await preview();
    expect(client.previewHistoryRetention).toHaveBeenLastCalledWith(expect.objectContaining({ policy: expect.objectContaining({ recordings: { mode: "max_age", max_age_days: 60 } }) }));
  });
  it("requires loss confirmation before enabling imported retention", async () => {
    const client = clientFixture({ suspended: true, recordings: { mode: "max_age", max_age_days: 30 } });
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await waitFor(() => expect(screen.getByText(/Imported retention is paused/)).toBeTruthy());
    await preview();
    confirm.mockResolvedValueOnce(false);
    fireEvent.click(screen.getByTestId("history-save"));
    await waitFor(() => expect(confirm).toHaveBeenCalled());
    expect(client.updateHistoryStorage).not.toHaveBeenCalled();
    expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining("Replay of removed recordings") }));
  });
  it("passes each reviewed batch token and describes references instead of physical reclaimed bytes", async () => {
    const client = clientFixture({ recordings: { mode: "max_age", max_age_days: 30 } });
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await preview();
    expect(screen.getByText(/do not guarantee an immediate reduction/)).toBeTruthy();
    fireEvent.click(screen.getByTestId("history-prune"));
    await waitFor(() => expect(client.pruneHistory).toHaveBeenCalledTimes(2));
    expect(client.pruneHistory.mock.calls.map(([request]) => request.preview_token)).toEqual(["review-1", "review-2"]);
    await waitFor(() => expect(screen.queryByTestId("history-preview-result")).toBeNull());
  });
  it("forces review again after a policy or reference conflict", async () => {
    const client = clientFixture();
    client.updateHistoryStorage.mockRejectedValueOnce(new Error("History changed"));
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await preview();
    fireEvent.click(screen.getByTestId("history-save"));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("Review a new preview"));
    expect(screen.queryByTestId("history-save")).toBeNull();
  });
  it("protects a selected archived task without changing ordinary pin state", async () => {
    const client = clientFixture();
    render(() => <HistoryStorageSettingsPanel client={client} />);
    await waitFor(() => expect(client.listProjects).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Project to protect" }));
    fireEvent.click(screen.getByRole("option", { name: "Example project" }));
    await waitFor(() => expect(client.listProjectSessions).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByLabelText("Show archived tasks")).not.toHaveProperty("disabled", true));
    fireEvent.click(screen.getByLabelText("Show archived tasks"));
    await waitFor(() => expect(client.listProjectSessions).toHaveBeenLastCalledWith("project-1", { archived: true, limit: 100 }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Task to protect" })).not.toHaveProperty("disabled", true));
    fireEvent.click(screen.getByRole("button", { name: "Task to protect" }));
    fireEvent.click(screen.getByRole("option", { name: "Old task" }));
    fireEvent.click(screen.getByRole("button", { name: "Protect history" }));
    await waitFor(() => expect(client.createHistoryProtection).toHaveBeenCalledWith({ scope_type: "session", scope_id: "task-1", protected: true }));
  });
  it.each([
    ["save", "close"], ["prune", "close"],
    ["save", "change client"], ["prune", "change client"],
  ])("cancels pending %s when choosing to %s", async (action, transition) => {
    const completion: { resolve?: (approved: boolean) => void } = {};
    const decision = new Promise<boolean>((resolve) => { completion.resolve = resolve; });
    confirm.mockReturnValueOnce(decision);
    const client = clientFixture({ recordings: { mode: "max_age", max_age_days: 30 } });
    const [activeClient, setActiveClient] = createSignal(client);
    const { unmount } = render(() => <HistoryStorageSettingsPanel client={activeClient()} />);
    await preview();
    fireEvent.click(screen.getByTestId(`history-${action}`));
    await waitFor(() => expect(confirm).toHaveBeenCalled());
    if (transition === "close") unmount();
    else {
      const replacement = clientFixture();
      setActiveClient(replacement);
      await waitFor(() => expect(replacement.getHistoryStorage).toHaveBeenCalled());
    }
    assert(completion.resolve, "confirmation resolver is installed");
    completion.resolve(true);
    await decision;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(client.pruneHistory).not.toHaveBeenCalled();
    expect(client.updateHistoryStorage).not.toHaveBeenCalled();
  });

});
