import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UpdatesSettingsPanel } from "./UpdatesSettingsPanel.tsx";
import { packageFixture, updateFixture, updateServiceFixture, stagedFixture } from "../../../settings/system/update-test-fixture.ts";
import { setConfirmDestructivePresenter } from "../../../platform/interaction/confirm-dialog.ts";

afterEach(() => { cleanup(); setConfirmDestructivePresenter(null); });
describe("update settings", () => {
  it("explains development availability and disables update controls", async () => {
    const capabilities = { can_check: false, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "development_build" };
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => updateFixture({ candidate: null, automatic_updates_enabled: false, capabilities }) })} />);
    expect((await screen.findByTestId("updates-development-build")).textContent).toContain("development builds");
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", true);
    expect(screen.getByRole("checkbox", { name: "Automatic updates" })).toHaveProperty("disabled", true);
    expect(screen.getByTestId("updates-channel")).toHaveProperty("disabled", true);
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });
  it("shows the rejected feed cause without claiming an installation failure", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => updateFixture({ last_error: { code: "feed_rejected", detail: "latest.json.sig returned HTTP 404" } }) })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("feed could not be verified");
    expect(screen.getByText("latest.json.sig returned HTTP 404")).toBeTruthy();
  });
  it("does not invoke native updates in a web app", () => {
    render(() => <UpdatesSettingsPanel />); expect(screen.getByTestId("updates-unavailable").textContent).toContain("desktop app");
  });
  it("downloads only the retained release identity", async () => {
    const download = vi.fn(async () => stagedFixture()); const service = updateServiceFixture({ download });
    render(() => <UpdatesSettingsPanel updateService={service} />);
    fireEvent.click(await screen.findByRole("button", { name: "Download update" }));
    await waitFor(() => expect(download).toHaveBeenCalledWith("release-1"));
    expect(await screen.findByRole("button", { name: "Restart to update" })).toBeTruthy();
  });
  it("requires confirmation only for an immediate restart", async () => {
    const restart = vi.fn(async () => {}); const confirm = vi.fn(async () => false); setConfirmDestructivePresenter(confirm);
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ restart, getState: async () => stagedFixture() })} />);
    fireEvent.click(await screen.findByRole("button", { name: "Restart to update" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledOnce()); expect(restart).not.toHaveBeenCalled();
    // The declined confirmation releases the controls before a second attempt.
    await waitFor(() => expect(screen.getByRole("button", { name: "Restart to update" })).toHaveProperty("disabled", false));
    confirm.mockResolvedValue(true); fireEvent.click(screen.getByRole("button", { name: "Restart to update" }));
    await waitFor(() => expect(restart).toHaveBeenCalledWith("release-1"));
  });
  it("uses native capabilities for package-managed installs", async () => {
    const state = updateFixture({ install_source: "homebrew_cask", capabilities: { can_check: true, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "package_managed" } });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByTestId("updates-brew-upgrade")).textContent).toContain("brew upgrade --cask painted-wolf-code");
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.queryByTestId("updates-package-identity")).toBeNull();
    expect(screen.queryByText(/installs them when you quit/)).not.toBeNull();
  });
  it("attributes a package-managed refusal to the package manager, not Homebrew", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => packageFixture({ last_error: { code: "package_managed" } }) })} />);
    expect((await screen.findByRole("alert")).textContent).toBe("Your package manager manages this installation. Use its upgrade command to update.");
  });
  it("names the system package and its exact upgrade command without offering a download", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => packageFixture() })} />);
    expect((await screen.findByTestId("updates-package-identity")).textContent).toBe("Installed from the painted-wolf-code 1.0.2 package (deb). Your package manager owns this installation.");
    const upgrade = screen.getByTestId("updates-package-upgrade");
    expect(upgrade.textContent).toBe("Version 1.1.0 is available. Run sudo apt update && sudo apt install --only-upgrade painted-wolf-code to update.");
    expect(upgrade.querySelector("code")?.textContent).toBe("sudo apt update && sudo apt install --only-upgrade painted-wolf-code");
    expect(screen.getAllByText(/Version 1.1.0 is available/)).toHaveLength(1);
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.queryByTestId("updates-brew-upgrade")).toBeNull();
    expect(screen.getByTestId("updates-package-hint").textContent).toContain("system package manager");
    expect(screen.queryByText(/installs them when you quit/)).toBeNull();
    expect(screen.getByTestId("updates-channel")).toHaveProperty("disabled", false);
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", false);
  });
  it("shows only the package identity when the package is current", async () => {
    const state = packageFixture({ candidate: null, discovery: "up_to_date" });
    state.package = { ...state.package!, family: "rpm", update_command: "sudo dnf upgrade painted-wolf-code" };
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByTestId("updates-package-identity")).textContent).toContain("package (rpm)");
    expect(screen.queryByTestId("updates-package-upgrade")).toBeNull();
    expect(screen.queryByTestId("updates-unpackaged")).toBeNull();
  });
  it("leaves Nix update ownership with the selected profile or configuration", async () => {
    const state = packageFixture();
    state.package = { ...state.package!, family: "nix", update_command: "Update the Nix profile, Home Manager configuration, or NixOS configuration that owns this installation." };
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    const upgrade = await screen.findByTestId("updates-package-upgrade");
    expect(upgrade.textContent).toContain("Nix profile, Home Manager configuration, or NixOS configuration");
    expect(upgrade.querySelector("code")).toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });
  it("uses a full pacman upgrade and leaves the AUR workflow explicit", async () => {
    const state = packageFixture();
    state.package = { ...state.package!, family: "pacman", update_command: "sudo pacman -Syu" };
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    const upgrade = await screen.findByTestId("updates-package-upgrade");
    expect(upgrade.querySelector("code")?.textContent).toBe("sudo pacman -Syu");
    expect(screen.getByText(/For an AUR installation/).textContent).toContain("workflow you selected");
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });
  it("explains an unpackaged Linux copy and its package-manager error", async () => {
    const capabilities = { can_check: true, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "unpackaged_installation" };
    const state = updateFixture({ install_source: "unpackaged", candidate: null, discovery: "idle", capabilities, last_error: { code: "unpackaged_installation" } });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByTestId("updates-unpackaged")).textContent).toContain("Install the .deb or .rpm package");
    expect(screen.getByRole("alert").textContent).toContain("no update command is available");
    expect(screen.queryByTestId("updates-package-identity")).toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });
  it("retains the existing preference control and changes its native authority", async () => {
    const setAutomaticUpdatesEnabled = vi.fn(async (enabled: boolean) => updateFixture({ automatic_updates_enabled: enabled }));
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ setAutomaticUpdatesEnabled })} />);
    const toggle = await screen.findByRole("checkbox", { name: "Automatic updates" });
    await waitFor(() => expect(toggle).toHaveProperty("disabled", false)); fireEvent.click(toggle);
    await waitFor(() => expect(setAutomaticUpdatesEnabled).toHaveBeenCalledWith(false));
  });
  it("renders signed-release notes as untrusted Markdown", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture()} />);
    expect((await screen.findByTestId("updates-release-notes")).textContent).toContain("A new release.");
  });
  it("keeps a committed recovery failure distinct from an installed update", async () => {
    const state = updateFixture({ installation: "recovery_required", last_error: { code: "recovery_required" }, capabilities: { can_check: false, can_download: false, can_restart_to_update: false, can_install_automatically: false, blocked_reason: "recovery_required" } });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("Close and reopen");
    expect(screen.queryByText(/The update is installed/)).toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
    expect(screen.getByRole("checkbox", { name: "Automatic updates" })).toHaveProperty("disabled", true);
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", true);
  });
  it("offers restart for a committed handoff and explains when it installs", async () => {
    const restart = vi.fn(async () => {}); setConfirmDestructivePresenter(async () => true);
    const state = stagedFixture(); state.installation = "committed"; state.capabilities = { ...state.capabilities, can_check: false };
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ restart, getState: async () => state })} />);
    expect((await screen.findByTestId("updates-installation-status")).textContent).toContain("running copies of this installation to quit");
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", true);
    fireEvent.click(screen.getByRole("button", { name: "Restart to update" }));
    await waitFor(() => expect(restart).toHaveBeenCalledWith("release-1"));
  });
  it("requires renewed host confirmation after a rejected feed", async () => {
    const confirmed = stagedFixture();
    const check = vi.fn(async () => confirmed);
    const rejected = stagedFixture({ offer_confirmed_at: null, last_error: { code: "feed_rejected" }, capabilities: { ...confirmed.capabilities, can_install_automatically: false } });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ check, getState: async () => rejected })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("Installation is paused");
    expect(screen.getByTestId("updates-installation-status").textContent).toContain("Check for updates again");
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    await waitFor(() => expect(check).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(screen.getByTestId("updates-installation-status").textContent).toContain("final safety check");
  });
  it("describes an explicitly staged update without promising an automatic install", async () => {
    const state = stagedFixture({ automatic_updates_enabled: false });
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => state })} />);
    expect((await screen.findByTestId("updates-installation-status")).textContent).toBe("Update downloaded. Restart to install it.");
  });
  it("holds every control while the restart confirmation is open", async () => {
    let decide!: (value: boolean) => void; const restart = vi.fn(async () => {});
    setConfirmDestructivePresenter(() => new Promise<boolean>((resolve) => { decide = resolve; }));
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ restart, getState: async () => stagedFixture() })} />);
    fireEvent.click(await screen.findByRole("button", { name: "Restart to update" }));
    await waitFor(() => expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", true));
    expect(screen.getByRole("checkbox", { name: "Automatic updates" })).toHaveProperty("disabled", true);
    decide(false);
    await waitFor(() => expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", false));
    expect(restart).not.toHaveBeenCalled();
  });
  it("shows typed recovery guidance and keeps diagnostic detail separate", async () => {
    render(() => <UpdatesSettingsPanel updateService={updateServiceFixture({ getState: async () => updateFixture({ last_error: { code: "disk_space", detail: "test detail" } }) })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("free space"); expect(screen.getByText("Technical details")).toBeTruthy();
  });
});
