import { cleanup, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { UpdatesSettingsPanel } from "./UpdatesSettingsPanel.tsx";
import { packageFixture, packageSourceFixture, updateServiceFixture } from "../../../settings/system/update-test-fixture.ts";
import type { PackageSourceFacts } from "../../../settings/system/update-service.ts";

afterEach(cleanup);
const renderSource = (over: Partial<PackageSourceFacts>) => {
  const state = packageFixture();
  state.package!.source = packageSourceFixture({ cached_only: true, checked_at: 100, database_owner: "painted-wolf-code", database_version: "1.1.0-1", architecture: "amd64", ...over });
  render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
};
describe("package-source observations", () => {
  it("separates an upgraded database and cached native candidate from the running product and upstream feed", async () => {
    renderSource({ availability: "candidate_found", selection_priority: 700, candidate: { version: "1.2.0~rc.1", architecture: "amd64", newer_than_installed: true,
      repositories: [{ id: "Painted Wolf Code", suite: "preview", trusted: true }] } });
    expect((await screen.findByTestId("updates-source-candidate")).textContent).toContain("Cached package candidate: 1.2.0~rc.1");
    expect(screen.getByTestId("updates-database-version").textContent).toContain("1.1.0-1");
    expect(screen.getByTestId("updates-database-owner").textContent).toContain("painted-wolf-code");
    expect(screen.getByTestId("updates-current-version").textContent).toBe("Version 1.0.2");
    expect(screen.getByTestId("updates-package-source").textContent).toContain("suite: preview");
    expect(screen.getByTestId("updates-source-priority").textContent).toContain("700");
    expect(screen.getByTestId("updates-package-upgrade").textContent).toContain("Upstream version 1.1.0");
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.getByTestId("updates-status").querySelector("code")).toBeNull();
  });
  it("reports held packages without suggesting that a cached candidate is installable", async () => {
    renderSource({ availability: "blocked", reason: "held", held: true });
    await screen.findByTestId("updates-source-reason");
    expect(screen.queryByTestId("updates-up-to-date")).toBeNull();
  });
  it("keeps DNF transaction policy unknown even when its query returns a candidate", async () => {
    renderSource({ availability: "unknown", reason: "policy_unobserved", candidate: { version: "1.2.0-1", architecture: "x86_64", newer_than_installed: true, repositories: [{ id: "chosen-repo", suite: null, trusted: null }] } });
    await screen.findByTestId("updates-source-reason");
    expect(screen.getByTestId("updates-source-candidate")).toBeTruthy();
  });
  it("reports a failed fresh source check without a stale candidate", async () => {
    renderSource({ availability: "failed", reason: "query_failed" });
    await screen.findByTestId("updates-source-reason");
    expect(screen.queryByTestId("updates-source-candidate")).toBeNull();
  });
  it("does not turn a current cached policy candidate into a latest-release claim", async () => {
    renderSource({ availability: "no_newer_candidate", candidate: { version: "1.1.0-1", architecture: "amd64", newer_than_installed: false,
      repositories: [{ id: "Painted Wolf Code", suite: "stable", trusted: true }] } });
    await screen.findByTestId("updates-source-candidate");
    expect(screen.queryByTestId("updates-up-to-date")).toBeNull();
  });
  it("reports an unknown owner without claiming that repository metadata was read", async () => {
    renderSource({ availability: "unknown", reason: "unsupported_owner", cached_only: false, database_owner: null, database_version: null, architecture: null });
    await screen.findByTestId("updates-source-reason");
    expect(screen.queryByTestId("updates-database-version")).toBeNull();
  });
  it("reports a changed database owner without claiming an update for the old running package", async () => {
    renderSource({ availability: "blocked", reason: "owner_changed", cached_only: false, database_owner: "replacement-package", database_version: null, architecture: null });
    await screen.findByTestId("updates-source-reason");
    expect(screen.getByTestId("updates-database-owner").textContent).toContain("replacement-package");
    expect(screen.getByTestId("updates-package-identity").textContent).toContain("painted-wolf-code 1.0.2");
    expect(screen.queryByTestId("updates-source-candidate")).toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });
  it("keeps an absent repository identity or suite unknown", async () => {
    renderSource({ availability: "candidate_found", candidate: { version: "1.2.0", architecture: "amd64", newer_than_installed: true,
      repositories: [{ id: "", suite: null, trusted: true }] } });
    const source = await screen.findByTestId("updates-package-source");
    expect(source.textContent).not.toContain("suite:");
  });
});
