import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { bindContextAction, contextAction } from "./context-actions.ts";
import { seedStockFrame } from "../contributions/stock-frame-test.ts";
import { resetContributionStoreForTest } from "../contributions/contribution-store.ts";

const publish = vi.hoisted(() => vi.fn());
vi.mock("../platform/connection/app-connection.ts", async (original) => ({
  ...await original<typeof import("../platform/connection/app-connection.ts")>(),
  appNoticeReporter: () => ({ publish }),
}));
beforeEach(() => publish.mockClear());
afterEach(resetContributionStoreForTest);

describe("context action dispatch", () => {
  it("disables native commands until the host frame exists", () => {
    resetContributionStoreForTest();
    const run = vi.fn();
    const item = contextAction("findInFile", { onSelect: run });
    expect(item.disabled).toBe(true);
    item.onSelect();
    expect(run).not.toHaveBeenCalled();
  });
  it("rechecks native command availability against current shell facts", () => {
    seedStockFrame();
    const run = vi.fn();
    const item = contextAction("inlineEdit", { onSelect: run });
    item.onSelect();
    expect(run).toHaveBeenCalledOnce();
    seedStockFrame({ filesStageActive: false });
    item.onSelect();
    expect(run).toHaveBeenCalledOnce();
  });
  it("does not execute disabled actions even when invoked directly", () => {
    const run = vi.fn();
    contextAction("copyPath", { disabled: true, onSelect: run }).onSelect();
    expect(run).not.toHaveBeenCalled();
  });
  it("binds the supplied target and keeps destructive semantics in the catalog", () => {
    const run = vi.fn();
    const item = contextAction("moveToTrash", { onSelect: () => run("root-a", "file.txt") });
    expect(item.danger).toBe(true);
    item.onSelect();
    expect(run).toHaveBeenCalledWith("root-a", "file.txt");
  });
  it("rechecks current command availability before dispatch", () => {
    let available = true;
    const run = vi.fn();
    const item = bindContextAction({ label: "Contributed action", onSelect: run }, () => available);
    available = false;
    item.onSelect();
    expect(run).not.toHaveBeenCalled();
  });
  it.each([false, true])("reports action failures once (async: %s)", async (async) => {
    const error = new Error("The item is unavailable.");
    const item = contextAction("open", {
      onSelect: async ? () => Promise.reject(error) : () => { throw error; },
    });
    item.onSelect();
    await vi.waitFor(() => expect(publish).toHaveBeenCalledOnce());
    expect(publish).toHaveBeenCalledWith(expect.objectContaining({ severity: "error", message: error.message }));
  });
});
