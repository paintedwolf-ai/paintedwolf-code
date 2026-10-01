import { setPreflightReport } from "../../../platform/persistence/preflight-report.ts";
import { resetPreflightStore } from "../../../platform/persistence/preflight-store.ts";
import { describe, expect, it, beforeEach, afterEach, vi } from "vitest";
import { findByTestId, fireEvent, render, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../../api/client.ts";
import type { PreflightReport } from "../../../api/types.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { PreflightProbeList } from "./PreflightProbeList.tsx";

const backend = vi.hoisted(() => ({ client: null as LycaonClient | null }));
vi.mock("../../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => backend.client }));

describe("PreflightProbeList", () => {
  beforeEach(() => resetPreflightStore());
  afterEach(() => { resetPreflightStore(); backend.client = null; });

  it("lists every probe, ok ones included", async () => {
    setPreflightReport({
      overall: "degraded",
      probes: [
        { id: "os_version", status: "ok" },
        { id: "git_engine", status: "degraded", code: "GIT_ENGINE_UNAVAILABLE", title: "Bundled git engine unavailable", message: "m", suggested_action: "Reinstall the app" },
      ],
    } as PreflightReport);

    const { baseElement, getByTestId, getByText } = render(() => <PreflightProbeList />);

    await findByTestId(baseElement as HTMLElement, "preflight-probe-os_version");
    expect(getByTestId("preflight-probe-git_engine")).toBeTruthy();
    expect(getByTestId("preflight-overall").textContent).toContain("degraded");
    expect(getByText("Reinstall the app")).toBeTruthy();
  });

  it("falls back to the probe id when the wire carries no copy", async () => {
    setPreflightReport({
      overall: "degraded",
      probes: [{ id: "scanner_engine", status: "degraded", code: "SCANNER_ENGINE_UNAVAILABLE" }],
    } as PreflightReport);

    const { baseElement, getByText } = render(() => <PreflightProbeList />);

    await findByTestId(baseElement as HTMLElement, "preflight-probe-scanner_engine");
    expect(getByText("scanner_engine")).toBeTruthy();
  });

  it("re-checks on demand", async () => {
    setPreflightReport({
      overall: "ok",
      probes: [{ id: "os_version", status: "ok" }],
    } as PreflightReport);
    let finish!: (report: PreflightReport) => void;
    const getPreflight = vi.fn(() => new Promise<PreflightReport>((resolve) => { finish = resolve; }));
    backend.client = stubClient({ getPreflight });

    const { baseElement, getByTestId } = render(() => <PreflightProbeList />);
    await findByTestId(baseElement as HTMLElement, "preflight-probe-os_version");

    const recheck = getByTestId("preflight-recheck") as HTMLButtonElement;
    expect(getPreflight).not.toHaveBeenCalled();
    fireEvent.click(recheck);
    expect(getPreflight).toHaveBeenCalledOnce();
    expect(recheck.disabled).toBe(true);

    finish({
      overall: "degraded",
      probes: [{ id: "os_version", status: "degraded" }],
    } as PreflightReport);
    await waitFor(() => {
      expect(getByTestId("preflight-status-os_version").textContent).toBe("degraded");
      expect(getByTestId("preflight-overall").textContent).toContain("degraded");
      expect(recheck.disabled).toBe(false);
    });
  });

  it("degrades to one line when no report is available", async () => {
    setPreflightReport(undefined as unknown as PreflightReport);

    const { baseElement } = render(() => <PreflightProbeList />);

    await findByTestId(baseElement as HTMLElement, "preflight-probes-unavailable");
  });
});
