import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { PreflightReport } from "../api/types.ts";
import { mockPreflightReport } from "../api/mocks/fixtures.ts";
import { clientNoticeError } from "./client-notices.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { resolveCriticalStop } from "./critical-stop-model.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";
import { shouldReportToNoticeRail } from "./report-policy.ts";

const denSrc = join(import.meta.dirname, "..");

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

  it("partitions on the host tier, never on probe status", () => {
    const strip = (src: string) => src.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, "");

    const nudge = strip(
      readFileSync(join(denSrc, "components/home/PreflightNudge.tsx"), "utf8"),
    );
    expect(nudge).toContain('"non_catastrophic"');
    expect(nudge).not.toContain('"degraded"');
    expect(nudge).not.toContain('"blocked"');

    const model = strip(
      readFileSync(join(denSrc, "notices/critical-stop-model.ts"), "utf8"),
    );
    expect(model).toContain('"catastrophic"');
    expect(model).not.toContain('"blocked"');
  });

  it("keeps unattributed project conditions off the Home readiness card", () => {
    const strip = (src: string) => src.replace(/\/\*[\s\S]*?\*\/|\/\/.*$/gm, "");

    const nudge = strip(
      readFileSync(join(denSrc, "components/home/PreflightNudge.tsx"), "utf8"),
    );
    expect(nudge).toContain('probe.scope !== "project"');
  });

  it("gates readiness from the app root, not per stage", () => {
    const root = readFileSync(join(denSrc, "App.tsx"), "utf8");
    expect(root).toContain("createCriticalStop");

    for (const file of [
      "components/shell/Shell.tsx",
      "components/chatview/ChatView.tsx",
    ]) {
      const src = readFileSync(join(denSrc, file), "utf8");
      for (const readinessStop of [
        "createCriticalStop",
        "resolveCriticalStop",
        "CriticalStopStage",
        "preflight-store.ts",
        "preflight-report.ts",
      ]) {
        expect(src).not.toContain(readinessStop);
      }
    }
  });

  it("scopes a failed render to its own stage", () => {
    const boundary = readFileSync(
      join(denSrc, "components/shell/StageErrorBoundary.tsx"),
      "utf8",
    );
    expect(boundary).toContain("ErrorBoundary");
    expect(boundary).toContain("CriticalStop");
    expect(boundary).not.toContain("resolveCriticalStop");
    expect(boundary).not.toContain("preflight");

    const copy = CLIENT_NOTICES.view_render_failed;
    expect(copy.scope).toBe("session");
    expect(copy.suggestedAction).toContain("rest of the app still works");

    const shell = readFileSync(
      join(denSrc, "components/shell/Shell.tsx"),
      "utf8",
    );
    expect(shell).toContain("StageErrorBoundary");
  });

  it("reads readiness from the shared store, not per surface", () => {
    for (const file of [
      "components/home/PreflightNudge.tsx",
      "components/settings/system/PreflightProbeList.tsx",
      "components/settings/system/SystemInfoPanel.tsx",
      "components/CriticalStopStage.tsx",
    ]) {
      const src = readFileSync(join(denSrc, file), "utf8");
      expect(src).not.toContain("getPreflight");
      expect(src).toMatch(/preflight-(report|store)\.ts/);
    }
  });

  it("mounts host notification cards in the top dock, never in the transcript", () => {
    const cards = [
      "NoticeRail",
      "NotificationStack",
      "NoProviderCard",
      "VerifyTestNudge",
      "SpendCeilingReachedNudge",
      "SpendCeilingApproachingNudge",
      "SessionProtectionBanners",
    ];
    const view = readFileSync(
      join(denSrc, "components/chatview/ChatView.tsx"),
      "utf8",
    );
    for (const card of cards) expect(view).not.toContain(card);

    const shell = readFileSync(
      join(denSrc, "components/shell/Shell.tsx"),
      "utf8",
    );
    expect(shell).toContain("ChatTopChromeStack");
    for (const card of cards) expect(shell).toContain(card);
  });

  it("mounts one shared standing-notification stack on Home and in chat", () => {
    const shell = readFileSync(
      join(denSrc, "components/shell/Shell.tsx"),
      "utf8",
    );
    const chrome = readFileSync(
      join(denSrc, "components/nav/ChatTopChromeStack.tsx"),
      "utf8",
    );

    const columns = readFileSync(join(denSrc, "components/shell/ShellColumns.tsx"), "utf8");
    expect(shell).toContain("const notificationStack = () =>");
    expect(shell).toContain("notifications={{");
    expect(shell).toContain("notifications={showHome() ? notificationStack() : null}");
    expect(columns).toContain("{props.notifications}");
    expect(chrome).toContain('slot="notifications"');
  });

  it("retires scoped notices when the thing they describe goes away", () => {
    const retire = readFileSync(join(denSrc, "lifecycle/entity-retire.ts"), "utf8");
    expect(retire).toContain("clearSession(");
    expect(retire).toContain("clearProject(");

    const connection = readFileSync(join(denSrc, "platform/connection/app-connection.ts"), "utf8");
    expect(connection).toContain("entityRetireRef?.session(");
    expect(connection).toContain("entityRetireRef?.project(");

    const shell = readFileSync(join(denSrc, "components/shell/Shell.tsx"), "utf8");
    const navigation = readFileSync(join(denSrc, "components/shell/session-navigation.ts"), "utf8");
    expect(navigation).toContain("getEntityRetire()?.session(");
    expect(shell).toContain("getEntityRetire()?.project(");

    const events = readFileSync(join(denSrc, "api/events.ts"), "utf8");
    expect(events).toContain("clearSessionHostErrorNotices");
    expect(events).toContain('idle_disposition === "completed"');
  });
});
