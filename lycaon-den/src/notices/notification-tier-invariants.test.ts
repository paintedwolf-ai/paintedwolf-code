import { describe, expect, it } from "vitest";
import type { PreflightReport } from "../api/types.ts";
import { mockPreflightReport } from "../api/mocks/fixtures.ts";
import { clientNoticeError } from "./client-notices.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { resolveCriticalStop } from "./critical-stop-model.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { shouldReportToNoticeRail } from "./report-policy.ts";

function report(
  overall: PreflightReport["overall"],
  probes: PreflightReport["probes"],
): PreflightReport {
  return mockPreflightReport(overall, probes);
}

describe("notification tier partition", () => {
  it("stops the stage only when unreachable or the host declares catastrophic", () => {
    expect(resolveCriticalStop({ sidecarStatus: "disconnected" })).toBeDefined();
    expect(
      resolveCriticalStop({
        sidecarStatus: "connected",
        preflight: report("blocked", [
          {
            id: "os_version",
            status: "blocked",
            tier: "catastrophic",
            code: "OS_BELOW_FLOOR",
          },
        ]),
      }),
    ).toBeDefined();

    expect(resolveCriticalStop({ sidecarStatus: "connected" })).toBeUndefined();
    expect(
      resolveCriticalStop({
        sidecarStatus: "connected",
        preflight: report("degraded", [
          {
            id: "browser_engine",
            status: "degraded",
            tier: "non_catastrophic",
            code: "BROWSER_ENGINE_UNAVAILABLE",
          },
        ]),
      }),
    ).toBeUndefined();
  });

  it("rails an engine the shell is restarting and stops the window once it gives up", () => {
    const exit = { signal: 9, description: "was killed by signal 9 (SIGKILL)" };
    expect(resolveCriticalStop({
      sidecarStatus: "disconnected",
      offlineAvailable: true,
      engine: { state: "restarting", exit, attempt: 1 },
    })).toBeUndefined();
    expect(CLIENT_NOTICES.engine_restarting.scope).toBe("app");
    expect(resolveCriticalStop({
      sidecarStatus: "disconnected",
      offlineAvailable: true,
      engine: { state: "stopped", exit },
    })?.code).toBe("engine_stopped");
  });

  it("keeps connectivity off the notice rail", () => {
    expect(
      shouldReportToNoticeRail(
        new BackendTransportError(
          new TypeError("localized"),
          "unreachable",
        ),
      ),
    ).toBe(false);
    expect(shouldReportToNoticeRail(clientNoticeError("offline"))).toBe(false);
    expect(shouldReportToNoticeRail(clientNoticeError("fetch_failure"))).toBe(
      false,
    );
  });
});
