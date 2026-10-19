import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UpdatesSettingsPanel } from "./UpdatesSettingsPanel.tsx";
import { packageFixture, updateServiceFixture } from "../../../settings/system/update-test-fixture.ts";
import type { NativeUpdateState } from "../../../settings/system/update-service.ts";

afterEach(cleanup);
const nix = (over: Partial<NativeUpdateState> = {}) => packageFixture({
  package: { family: "nix", name: "painted-wolf-code", version: "1.0.2", update_command: "Review the owning Nix source.", source: { availability: "not_checked", owner: null, pinned: null } },
  ...over,
});

describe("Nix update ownership", () => {
  it("separates the running build, upstream offer and unknown selected source without an upgrade command", async () => {
    const download = vi.fn(); const restart = vi.fn(); const retry = vi.fn();
    const check = vi.fn(async () => nix());
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => nix(), check, download, restart, retry })} />);
    expect((await screen.findByTestId("updates-package-identity")).textContent).toContain("Running Nix build painted-wolf-code 1.0.2");
    expect(screen.getByTestId("updates-package-identity").textContent).toContain("owns");
    expect(screen.getByTestId("updates-package-identity").textContent).toContain("unknown");
    expect(screen.getByTestId("updates-package-upgrade").textContent).toContain("Upstream version 1.1.0 is available");
    expect(screen.getByTestId("updates-package-upgrade").textContent).toContain("selected Nix source has not been checked");
    expect(screen.getByTestId("updates-nix-owner").textContent).toContain("temporary launch");
    expect(screen.getByTestId("updates-nix-pin").textContent).toContain("unknown");
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.getByTestId("updates-status").querySelector("code")).toBeNull();
    expect(screen.getByTestId("updates-package-channel").textContent).toContain("does not change your Nix inputs");
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    await waitFor(() => expect(check).toHaveBeenCalledOnce());
    expect(download).not.toHaveBeenCalled(); expect(restart).not.toHaveBeenCalled(); expect(retry).not.toHaveBeenCalled();
  });

  it("labels retained upstream metadata after a failed check", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => nix({ discovery: "failed", last_error: { code: "feed_rejected" } }) })} />);
    const notice = await screen.findByTestId("updates-package-upgrade");
    expect(notice.textContent).toContain("Previously seen upstream version 1.1.0");
    expect(notice.textContent).toContain("Recheck the upstream feed");
    expect(notice.textContent).not.toContain("is available");
    expect(screen.getByTestId("updates-nix-pin").textContent).toContain("unknown");
  });

  it("withdraws the notice without changing a declared pin or offering installation", async () => {
    const state = nix(); state.package!.source.pinned = true;
    const check = async () => ({ ...state, candidate: null, discovery: "up_to_date" as const });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state, check })} />);
    expect((await screen.findByTestId("updates-nix-pin")).textContent).toContain("does not change its pin");
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    await waitFor(() => expect(screen.queryByTestId("updates-package-upgrade")).toBeNull());
    expect(screen.getByTestId("updates-nix-pin").textContent).toContain("pinned");
    expect(screen.getByText(/Package-source availability has not been checked/)).not.toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });

  it("keeps a preview offer separate from the selected Nix input", async () => {
    const state = nix({ channel: "preview" }); state.candidate!.version = "1.2.0-rc.1";
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByTestId("updates-package-upgrade")).textContent).toContain("Upstream version 1.2.0-rc.1");
    expect(screen.getByTestId("updates-package-channel").textContent).toContain("lock files, profiles or system generation");
    expect(screen.getByTestId("updates-package-identity").textContent).toContain("unknown");
  });
});
