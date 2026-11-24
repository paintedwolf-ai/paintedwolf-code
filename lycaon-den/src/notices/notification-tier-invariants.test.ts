import { describe, expect, it } from "vitest";
import type { PreflightReport } from "../api/types.ts";
import { mockPreflightReport } from "../api/mocks/fixtures.ts";
import { clientNoticeError } from "./client-notices.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { resolveCriticalStop } from "./critical-stop-model.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { denSourceRoot, loadSourceCorpus } from "../test/source-corpus.ts";
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

  it("mounts watch proposal cards only in the chat top dock proposal slot and never as notices or transcript cards", () => {
    const corpus = loadSourceCorpus(denSourceRoot, {
      extensions: [".ts", ".tsx"],
      excludeTests: true,
    });
    const proposalImporters = corpus.files
      .filter(
        (file) =>
          file.rel !== "components/watches/WatchProposalStack.tsx" &&
          file.text.includes("WatchProposalStack"),
      )
      .map((file) => file.rel);

    expect(proposalImporters).toEqual(["components/shell/ChatWorkspace.tsx"]);

    const proposalFile = corpus.files.find(
      (file) => file.rel === "components/watches/WatchProposalStack.tsx",
    )!;
    expect(proposalFile.text).not.toContain("notice-store");
    expect(proposalFile.text).not.toContain("NoticeStore");

    const dockStack = corpus.files.find(
      (file) => file.rel === "components/nav/ChatTopChromeStack.tsx",
    )!;
    const notificationsIdx = dockStack.text.indexOf('slot="notifications"');
    const proposalIdx = dockStack.text.indexOf('slot="proposal"');
    const verifyIdx = dockStack.text.indexOf('slot="verify"');
    expect(notificationsIdx).toBeGreaterThan(-1);
    expect(proposalIdx).toBeGreaterThan(notificationsIdx);
    expect(verifyIdx).toBeGreaterThan(proposalIdx);

    expect(CLIENT_NOTICES.standing_watch_paused.scope).toBe("project");
    expect(CLIENT_NOTICES.standing_watch_disabled.scope).toBe("project");
  });
});
