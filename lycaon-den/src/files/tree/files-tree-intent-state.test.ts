// @vitest-environment jsdom
import { beforeEach, expect, it, vi } from "vitest";
import { clientIdentity } from "../../platform/connection/client-identity.ts";
import { loadSharedAppState, resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { mergePeerTreeIntents, parseFilesTreeIntentState, restoredFilesTreeDisclosures, saveFilesTreeIntent } from "./files-tree-intent-state.ts";


beforeEach(() => {
  localStorage.clear();
  resetAppStateSnapshotForTests();
  vi.restoreAllMocks();
});

const expanded = () => [
  { address: { root_id: "root", path: "." }, open: true, recursive: true },
  { address: { root_id: "root", path: "generated" }, open: false, recursive: true },
];

it("restores recursive expansion and collapsed exceptions from a saved immutable configuration", async () => {
  const intent = expanded();
  await saveFilesTreeIntent("workspace", "revision", intent);
  intent[0]!.open = false;
  resetAppStateSnapshotForTests();
  await loadSharedAppState();
  expect(restoredFilesTreeDisclosures("workspace")).toEqual(expanded());
  const copy = restoredFilesTreeDisclosures("workspace")!;
  copy[0]!.open = false;
  expect(restoredFilesTreeDisclosures("workspace")).toEqual(expanded());
});

it("serializes simultaneous workspace saves without losing either configuration", async () => {
  await Promise.all([
    saveFilesTreeIntent("first", "one", expanded()),
    saveFilesTreeIntent("second", "two", []),
  ]);
  resetAppStateSnapshotForTests();
  await loadSharedAppState();
  expect(restoredFilesTreeDisclosures("first")).toEqual(expanded());
  expect(restoredFilesTreeDisclosures("second")).toEqual([]);
});

it("retries a failed save even when its revision is already in the local snapshot", async () => {
  vi.spyOn(localStorage, "setItem").mockImplementationOnce(() => { throw new DOMException("Disk unavailable", "QuotaExceededError"); });
  await expect(saveFilesTreeIntent("workspace", "one", expanded())).rejects.toThrow("Disk unavailable");
  await saveFilesTreeIntent("workspace", "one", expanded());
  resetAppStateSnapshotForTests();
  await loadSharedAppState();
  expect(restoredFilesTreeDisclosures("workspace")).toEqual(expanded());
});

it("refuses an invalid configuration as a whole instead of changing its meaning", () => {
  const record = (disclosures: unknown) => ({ byWindow: { [clientIdentity()]: { byWorkspace: {
    workspace: { revision: "one", touchedAt: 1, disclosures },
  } } } });
  expect(parseFilesTreeIntentState(record(expanded()))).toBeDefined();
  expect(parseFilesTreeIntentState(record([...expanded(), { address: { root_id: "root", path: "../escape" }, open: true, recursive: true }]))).toBeUndefined();
  expect(parseFilesTreeIntentState(record(Array.from({ length: 2049 }, () => expanded()[0])))).toBeUndefined();
});


it("keeps this window's pending configuration when a peer publishes an older copy", () => {
  const local = { byWorkspace: { workspace: { revision: "new", disclosures: expanded(), touchedAt: 2 } }, touchedAt: 2 };
  const peer = { byWorkspace: { workspace: { revision: "peer", disclosures: [], touchedAt: 3 } }, touchedAt: 3 };
  const incoming = { byWindow: { main: { ...local, byWorkspace: { workspace: { ...local.byWorkspace.workspace, revision: "old" } } }, peer } };
  const merged = mergePeerTreeIntents({ byWindow: { main: local } }, incoming, "main");
  expect(merged?.byWindow.main).toBe(local);
  expect(merged?.byWindow.peer).toEqual(peer);
});
