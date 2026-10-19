import { cleanup, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { UpdatesSettingsPanel } from "./UpdatesSettingsPanel.tsx";
import { packageFixture, packageSourceFixture, updateServiceFixture } from "../../../settings/system/update-test-fixture.ts";
import type { PackageSourceFacts } from "../../../settings/system/update-service.ts";

afterEach(cleanup);
const renderSource = (over: Partial<PackageSourceFacts>) => {
  const state = packageFixture();
  state.package!.source = packageSourceFixture({ cached_only: true, checked_at: 100, database_version: "1.1.0-1", architecture: "amd64", ...over });
  render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
};
describe("package-source observations", () => {
  it("separates an upgraded database and cached native candidate from the running product and upstream feed", async () => {
    renderSource({ availability: "candidate_found", candidate: { version: "1.2.0~rc.1", architecture: "amd64", newer_than_installed: true,
      repositories: [{ id: "Painted Wolf Code", suite: "preview", trusted: true }] } });
    expect((await screen.findByTestId("updates-source-candidate")).textContent).toContain("Cached package candidate: 1.2.0~rc.1");
    expect(screen.getByTestId("updates-database-version").textContent).toContain("1.1.0-1");
    expect(screen.getByTestId("updates-current-version").textContent).toBe("Version 1.0.2");
    expect(screen.getByTestId("updates-package-source").textContent).toContain("suite: preview");
    expect(screen.getByTestId("updates-package-source").textContent).toContain("without refreshing");
    expect(screen.getByTestId("updates-package-upgrade").textContent).toContain("Upstream version 1.1.0");
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.getByTestId("updates-status").querySelector("code")).toBeNull();
  });
  it("reports held packages without suggesting that a cached candidate is installable", async () => {
    renderSource({ availability: "blocked", reason: "held", held: true });
    expect((await screen.findByTestId("updates-source-reason")).textContent).toContain("holds this package back");
    expect(screen.queryByText("You’re using the latest version.")).toBeNull();
  });
  it("keeps DNF transaction policy unknown even when its query returns a candidate", async () => {
    renderSource({ availability: "unknown", reason: "policy_unobserved", candidate: { version: "1.2.0-1", architecture: "x86_64", newer_than_installed: true, repositories: [{ id: "chosen-repo", suite: null, trusted: null }] } });
    expect((await screen.findByTestId("updates-source-reason")).textContent).toContain("version locks or transaction policy");
    expect(screen.getByTestId("updates-source-candidate")).toBeTruthy();
  });
  it("reports a failed fresh source check without a stale candidate", async () => {
    renderSource({ availability: "failed", reason: "query_failed" });
    expect((await screen.findByTestId("updates-source-reason")).textContent).toContain("Earlier source results are no longer current");
    expect(screen.queryByTestId("updates-source-candidate")).toBeNull();
  });
});
