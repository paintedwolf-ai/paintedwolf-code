import { afterEach, describe, expect, it, beforeEach, vi } from "vitest";
import { findByTestId, render, waitFor } from "@solidjs/testing-library";
import type { PreflightReport } from "../../api/types.ts";
import { PreflightNudge } from "./PreflightNudge.tsx";
import { resetPreflightDismissalsForTests } from "./preflight-dismissals.ts";
import { resetNoticeActionSinksForTest, setShellNoticeActionSinks } from "../../notices/notice-actions.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import { resetPreflightStore } from "../../platform/persistence/preflight-store.ts";

// Specs seed the shared readiness store the card reads.

function report(...probes: PreflightReport["probes"]): PreflightReport {
  const rank = (s: string) => (s === "blocked" ? 2 : s === "degraded" ? 1 : 0);
  const overall = probes.reduce(
    (worst, p) => (rank(p.status) > rank(worst) ? p.status : worst),
    "ok" as string,
  );
  return { overall, probes } as PreflightReport;
}

describe("PreflightNudge", () => {
  beforeEach(() => {
    resetPreflightDismissalsForTests();
    resetPreflightStore();
    resetNoticeActionSinksForTest();
  });

  afterEach(() => resetNoticeActionSinksForTest());

  it("renders nothing when every probe is ok", async () => {
    setPreflightReport(report({ id: "os_version", status: "ok" }));

    const { container } = render(() => <PreflightNudge />);
    expect(container.querySelector('[data-testid="preflight-nudge"]')).toBeNull();
  });

  it("shows wire copy for a degraded probe and never invents its own", async () => {
    setPreflightReport(
      report(
        { id: "disk_space", status: "degraded", tier: "non_catastrophic", code: "DISK_SPACE_LOW", title: "Low disk space", message: "Only 900 MB is free." },
      ),
    );

    const { baseElement, getByText } = render(() => <PreflightNudge />);

    await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    expect(getByText("Low disk space")).toBeTruthy();
    expect(getByText("Only 900 MB is free.")).toBeTruthy();
  });

  it("ignores a catastrophic probe entirely — that tier is CriticalStop's", async () => {
    setPreflightReport(
      report({ id: "os_version", status: "blocked", tier: "catastrophic", code: "OS_BELOW_FLOOR", title: "Unsupported macOS", message: "m" }),
    );

    const { baseElement } = render(() => <PreflightNudge />);
    expect(baseElement.querySelector('[data-testid="preflight-nudge"]')).toBeNull();
  });

  // Blocked must not mask a degraded probe the card is still responsible for.
  it("shows a non-catastrophic probe even when another is catastrophic", async () => {
    setPreflightReport(
      report(
        { id: "os_version", status: "blocked", tier: "catastrophic", code: "OS_BELOW_FLOOR", title: "Unsupported macOS", message: "m" },
        { id: "disk_space", status: "degraded", tier: "non_catastrophic", code: "DISK_SPACE_LOW", title: "Low disk space", message: "Only 900 MB is free." },
      ),
    );

    const { baseElement, getByText } = render(() => <PreflightNudge />);

    await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    expect(getByText("Low disk space")).toBeTruthy();
  });

  it("dismisses a degraded probe for this live app process", async () => {
    const degraded = report({
      id: "provider_configured",
      status: "degraded",
      tier: "non_catastrophic",
      code: "NO_PROVIDER_CONFIGURED",
      title: "No AI provider configured",
      message: "m",
    });
    setPreflightReport(degraded);

    const first = render(() => <PreflightNudge />);
    const card = await findByTestId(first.baseElement as HTMLElement, "preflight-nudge");

    const dismiss = card.querySelector<HTMLElement>('[data-testid="system-nudge-dismiss"]');
    expect(dismiss).not.toBeNull();
    dismiss?.click();

    await waitFor(() =>
      expect(
        first.baseElement.querySelector('[data-testid="preflight-nudge"]'),
      ).toBeNull(),
    );
    first.unmount();

    // A fresh mount in this process respects the live dismissal. Restarting the
    // app resets it, so a machine condition cannot disappear indefinitely.
    const second = render(() => <PreflightNudge />);
    expect(
      second.baseElement.querySelector('[data-testid="preflight-nudge"]'),
    ).toBeNull();
  });

  it("shows the host's remedy under the message", async () => {
    setPreflightReport(
      report({ id: "git_engine", status: "degraded", tier: "non_catastrophic", code: "GIT_ENGINE_UNAVAILABLE", title: "Bundled git engine unavailable", message: "Bundled git is unavailable.", suggested_action: "Reinstall the app." }),
    );

    const { baseElement, getByText } = render(() => <PreflightNudge />);

    await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    expect(getByText("Reinstall the app.")).toBeTruthy();
  });

  it("uses a structured catalog action for an in-app remedy", async () => {
    const openProviders = vi.fn();
    setShellNoticeActionSinks({ openAIProviders: openProviders });
    setPreflightReport(
      report({
        id: "lite_model",
        status: "degraded",
        tier: "non_catastrophic",
        code: "LITE_UNAVAILABLE",
        title: "Helper unavailable",
        message: "m",
        actions: ["open_ai_providers"],
      }),
    );

    const { baseElement } = render(() => <PreflightNudge />);
    const card = await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    card.querySelector<HTMLElement>("[data-testid='system-nudge-primary']")?.click();

    expect(openProviders).toHaveBeenCalledOnce();
  });

  it("renders a browser usability failure as a Home card", async () => {
    setPreflightReport(
      report({
        id: "browser_engine",
        status: "degraded",
        tier: "non_catastrophic",
        code: "BROWSER_ENGINE_UNAVAILABLE",
        title: "Host browser title",
        message: "Host browser message.",
        suggested_action: "Host browser remedy.",
      }),
    );

    const { baseElement, getByText } = render(() => <PreflightNudge />);

    await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    expect(getByText("Host browser title")).toBeTruthy();
    expect(getByText("Host browser message.")).toBeTruthy();
    expect(getByText("Host browser remedy.")).toBeTruthy();
  });

  it("skips a code another card already states and takes the next probe", async () => {
    setPreflightReport(
      report(
        { id: "provider_configured", status: "degraded", tier: "non_catastrophic", code: "NO_PROVIDER_CONFIGURED", title: "No AI provider configured", message: "m" },
        { id: "disk_space", status: "degraded", tier: "non_catastrophic", code: "DISK_SPACE_LOW", title: "Low disk space", message: "Only 900 MB is free." },
      ),
    );

    const { baseElement, getByText, queryByText } = render(() => (
      <PreflightNudge statedElsewhere={["NO_PROVIDER_CONFIGURED"]} />
    ));

    await findByTestId(baseElement as HTMLElement, "preflight-nudge");
    expect(getByText("Low disk space")).toBeTruthy();
    expect(queryByText("No AI provider configured")).toBeNull();
  });

  it("shows nothing when the only probe is stated elsewhere", async () => {
    setPreflightReport(
      report({ id: "provider_configured", status: "degraded", tier: "non_catastrophic", code: "NO_PROVIDER_CONFIGURED", title: "No AI provider configured", message: "m" }),
    );

    const { baseElement } = render(() => (
      <PreflightNudge statedElsewhere={["NO_PROVIDER_CONFIGURED"]} />
    ));
    expect(baseElement.querySelector('[data-testid="preflight-nudge"]')).toBeNull();
  });

  it("shows nothing when the readiness check itself fails", async () => {
    setPreflightReport(undefined);

    const { baseElement } = render(() => <PreflightNudge />);
    expect(baseElement.querySelector('[data-testid="preflight-nudge"]')).toBeNull();
  });

  // Each refresh deserializes new objects with the same probe id.
  it("does not remount across a same-probe readiness refresh", async () => {
    setPreflightReport(
      report({
        id: "disk_space",
        status: "degraded",
        tier: "non_catastrophic",
        code: "DISK_SPACE_LOW",
        title: "Low disk space",
        message: "Only 900 MB is free.",
      }),
    );

    const { baseElement } = render(() => <PreflightNudge />);
    const cardBefore = await findByTestId(
      baseElement as HTMLElement,
      "preflight-nudge",
    );

    // A fresh report object with identical content, as a re-poll produces.
    setPreflightReport(
      report({
        id: "disk_space",
        status: "degraded",
        tier: "non_catastrophic",
        code: "DISK_SPACE_LOW",
        title: "Low disk space",
        message: "Only 850 MB is free.",
      }),
    );

    await waitFor(() => {
      expect(
        baseElement.querySelector('[data-testid="preflight-nudge"]')
          ?.textContent,
      ).toContain("Only 850 MB is free.");
    });
    expect(
      baseElement.querySelector('[data-testid="preflight-nudge"]'),
    ).toBe(cardBefore);
  });

  it("returns when the same probe reports a different code", async () => {
    setPreflightReport(
      report({ id: "git_engine", status: "degraded", tier: "non_catastrophic", code: "GIT_ENGINE_UNAVAILABLE", title: "Bundled git engine unavailable", message: "m" }),
    );
    const first = render(() => <PreflightNudge />);
    const card = await findByTestId(first.baseElement as HTMLElement, "preflight-nudge");
    card.querySelector<HTMLElement>('[data-testid="system-nudge-dismiss"]')?.click();
    first.unmount();

    // Same probe id, different code — dismissal is per (id, code), so the card returns.
    setPreflightReport(
      report({ id: "git_engine", status: "degraded", tier: "non_catastrophic", code: "DISK_SPACE_LOW", title: "Low disk space", message: "m" }),
    );
    const second = render(() => <PreflightNudge />);

    await findByTestId(second.baseElement as HTMLElement, "preflight-nudge");
    expect(second.getByText("Low disk space")).toBeTruthy();
  });
});
