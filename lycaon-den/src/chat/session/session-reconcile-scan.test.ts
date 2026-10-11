import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { createAppStore } from "../../store/app-state.ts";
import { refreshCodeScanCache } from "./session-reconcile.ts";
import { LycaonApiError } from "../../api/http.ts";

describe("refreshCodeScanCache", () => {
  it("clears cached scan when the row no longer exists", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", owner_person_id: "owner", activity_at: "2026-09-28T00:00:00Z", project_id: "project", posture: "build", status: "idle", created_at: "2026-09-28T00:00:00Z", updated_at: "2026-09-28T00:00:00Z" });
    store.actions.addCodeScan({
      scan_id: "scan-old",
      status: "complete",
      categories: ["sast"],
      findings_count: 0,
      long_running: false,
    });
    const client = stubClient({
      getCodeScan: vi.fn(async () => {
        throw new LycaonApiError("missing", 404, "scan_not_found");
      }),
    });

    await refreshCodeScanCache(store, client);
    expect(client.getCodeScan).toHaveBeenCalledWith("project", "scan-old", "summary");
    expect(store.state.latestCodeScan).toBeUndefined();
  });

  it("keeps cached scan when the row still exists", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ id: "session", owner_person_id: "owner", activity_at: "2026-09-28T00:00:00Z", project_id: "project", posture: "build", status: "idle", created_at: "2026-09-28T00:00:00Z", updated_at: "2026-09-28T00:00:00Z" });
    store.actions.addCodeScan({
      scan_id: "scan-live",
      status: "complete",
      categories: ["sast"],
      findings_count: 2,
      long_running: false,
    });
    const client = stubClient({
      getCodeScan: vi.fn(async () => ({
        id: "scan-live",
        status: "complete",
        categories: ["sast"],
      })),
    });

    await refreshCodeScanCache(store, client);
    expect(client.getCodeScan).toHaveBeenCalledWith("project", "scan-live", "summary");
    expect(store.state.latestCodeScan?.scan_id).toBe("scan-live");
  });
});
