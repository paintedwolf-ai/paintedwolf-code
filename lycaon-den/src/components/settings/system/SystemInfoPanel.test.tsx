import { setPreflightReport } from "../../../platform/persistence/preflight-report.ts";
import { resetPreflightStore } from "../../../platform/persistence/preflight-store.ts";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { findByTestId, render } from "@solidjs/testing-library";
import type { PreflightReport } from "../../../api/types.ts";
import { mockAttachmentCapabilities } from "../../../api/mocks/fixtures.ts";
import { SystemInfoPanel } from "./SystemInfoPanel.tsx";

vi.mock("../../../platform/connection/health.ts", () => ({
  lastSeenHostVersion: () => "0.1.0",
  lastSeenSchemaVersion: () => 12,
}));

describe("SystemInfoPanel", () => {
  beforeEach(() => resetPreflightStore());

  it("summarises version, schema, and every probe", async () => {
    setPreflightReport({
      overall: "degraded",
      attachment_capabilities: mockAttachmentCapabilities,
      probes: [
        { id: "os_version", status: "ok" },
        { id: "git_engine", status: "degraded", code: "GIT_ENGINE_UNAVAILABLE" },
      ],
    } as PreflightReport);

    const { baseElement } = render(() => <SystemInfoPanel />);

    const summary = await findByTestId(baseElement as HTMLElement, "system-info-summary");
    await vi.waitFor(() => expect(summary.textContent).toContain("git_engine"));

    expect(summary.textContent).toContain("0.1.0");
    expect(summary.textContent).toContain("12");
    expect(summary.textContent).toContain("degraded");
    expect(summary.textContent).toContain("GIT_ENGINE_UNAVAILABLE");
  });

  // Summary must omit secrets and machine-identifying paths.
  it("never prints a secret or a home path", async () => {
    setPreflightReport({
      overall: "degraded",
      attachment_capabilities: mockAttachmentCapabilities,
      probes: [
        {
          id: "config_dir",
          status: "blocked",
          code: "CONFIG_DIR_UNWRITABLE",
          detail: { reason: "write" },
          message: "The app cannot write to its configuration folder.",
        },
      ],
    } as PreflightReport);

    const { baseElement } = render(() => <SystemInfoPanel />);
    const summary = await findByTestId(baseElement as HTMLElement, "system-info-summary");
    await vi.waitFor(() => expect(summary.textContent).toContain("config_dir"));

    const text = summary.textContent ?? "";
    for (const forbidden of ["sk-", "Bearer ", "api_key", "/Users/"]) {
      expect(text).not.toContain(forbidden);
    }
  });

  it("still reports versions when readiness is unavailable", async () => {
    setPreflightReport(undefined as unknown as PreflightReport);

    const { baseElement } = render(() => <SystemInfoPanel />);

    const summary = await findByTestId(baseElement as HTMLElement, "system-info-summary");
    expect(summary.textContent).toContain("0.1.0");
    await vi.waitFor(() =>
      expect(summary.textContent).toContain("readiness: unavailable"),
    );
  });
});
