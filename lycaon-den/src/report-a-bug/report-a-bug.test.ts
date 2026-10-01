import { describe, expect, it, vi } from "vitest";
import { ISSUES_URL } from "../../shared/brand.ts";
import type { BackendConnection } from "../platform/connection/backend.ts";
import {
  applySaveResult,
  initialReportBugStep,
  openIssuesPage,
  saveReportBundle,
} from "./report-a-bug.ts";

const connection = {
  baseUrl: "http://127.0.0.1:8787",
  apiToken: "t",
} as BackendConnection;

describe("report-a-bug model", () => {
  it("starts on explain", () => {
    expect(initialReportBugStep()).toEqual({ kind: "explain" });
  });

  it("maps a successful save to saved(path)", () => {
    expect(
      applySaveResult({ kind: "saved", path: "/tmp/report.zip" }),
    ).toEqual({ kind: "saved", path: "/tmp/report.zip" });
  });

  it("maps cancel back to explain without a path", () => {
    expect(applySaveResult({ kind: "cancelled" })).toEqual({
      kind: "explain",
    });
  });

  it("saveReportBundle: success → saved(path)", async () => {
    const save = vi.fn(async () => ({
      kind: "saved" as const,
      path: "/Users/me/Downloads/bundle.zip",
    }));
    const step = await saveReportBundle(connection, save);
    expect(step).toEqual({
      kind: "saved",
      path: "/Users/me/Downloads/bundle.zip",
    });
    expect(save).toHaveBeenCalledWith(connection);
  });

  it("saveReportBundle: cancel → explain", async () => {
    const save = vi.fn(async () => ({ kind: "cancelled" as const }));
    const step = await saveReportBundle(connection, save);
    expect(step).toEqual({ kind: "explain" });
  });

  it("openIssuesPage targets ISSUES_URL only", async () => {
    const open = vi.fn(async (url: string) => {
      expect(url).toBe(ISSUES_URL);
      return true;
    });
    await openIssuesPage(open);
    expect(open).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith(ISSUES_URL);
  });

  it("never POSTs the bundle or report text", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const save = vi.fn(async () => ({
      kind: "saved" as const,
      path: "/tmp/x.zip",
    }));
    await saveReportBundle(connection, save);
    const open = vi.fn(async () => true);
    await openIssuesPage(open);
    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
  });
});
