import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { PreflightReport } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { preflightReport } from "./preflight-report.ts";
import { preflightRefreshing, refreshPreflight, resetPreflightStore } from "./preflight-store.ts";

const backend = vi.hoisted(() => ({ client: null as LycaonClient | null }));
vi.mock("../connection/app-connection.ts", () => ({ getLycaonClient: () => backend.client }));
// The shared setup seeds a report for every test; ordering starts from none.
beforeEach(() => resetPreflightStore());
afterEach(() => { resetPreflightStore(); backend.client = null; });

function report(overall: PreflightReport["overall"]): PreflightReport {
  return { overall, probes: [], attachment_capabilities: {} as PreflightReport["attachment_capabilities"] };
}

describe("preflight request ordering", () => {
  it("clears the previous backend's published verdict before a failed replacement read", async () => {
    backend.client = stubClient({ getPreflight: async () => report("blocked") });
    await refreshPreflight();
    resetPreflightStore();
    backend.client = stubClient({
      getPreflight: async () => {
        throw new Error("offline");
      },
    });
    await refreshPreflight();
    expect(preflightReport()).toBeUndefined();
  });

  it.each(["older first", "newer first"])("keeps a replacement backend's report when responses finish %s", async (order) => {
    let answerOld!: (report: PreflightReport) => void;
    let answerNew!: (report: PreflightReport) => void;
    backend.client = stubClient({ getPreflight: () => new Promise<PreflightReport>((resolve) => { answerOld = resolve; }) });
    const old = refreshPreflight();
    expect(refreshPreflight()).toBe(old);
    backend.client = stubClient({ getPreflight: () => new Promise<PreflightReport>((resolve) => { answerNew = resolve; }) });
    const fresh = refreshPreflight();
    expect(fresh).not.toBe(old);
    if (order === "older first") {
      answerOld(report("blocked"));
      await expect(old).resolves.toBe("unavailable");
      expect(preflightRefreshing()).toBe(true);
      expect(preflightReport()).toBeUndefined();
      answerNew(report("ok"));
    } else {
      answerNew(report("ok"));
      await expect(fresh).resolves.toBe("refreshed");
      answerOld(report("blocked"));
    }
    await Promise.all([old, fresh]);
    expect(preflightReport()?.overall).toBe("ok");
    expect(preflightRefreshing()).toBe(false);
  });
});
