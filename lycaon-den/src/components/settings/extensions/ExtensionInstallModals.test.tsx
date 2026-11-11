import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { ExtensionPackSummary } from "../../../api/types.ts";
import { ExtensionInstallModals } from "./ExtensionInstallModals.tsx";

const samplePack: ExtensionPackSummary = {
  id: "acme/extras",
  name: "ACME extras",
  version: "1.0.0",
  extension_api: "^1.0.0",
  installation_state: "release",
  installation_scope: "device",
  dependencies: {},
  kind: "git",
  enabled: true,
  removable: true,
  unit_count: 1,
  has_profile: true,
  contributing: true,
  source: "https://github.com/acme/extras",
  meta_pack_ids: [],
};

describe("ExtensionInstallModals", () => {
  it("handles add, suite install, and profile modal inputs and dismissals", () => {
    const [addOpen, setAddOpen] = createSignal(true);
    const [addUrl, setAddUrl] = createSignal("");
    const [addVersion, setAddVersion] = createSignal("");
    const [addRef, setAddRef] = createSignal("");
    const [suiteOpen, setSuiteOpen] = createSignal(true);
    const [suiteUrl, setSuiteUrl] = createSignal("");
    const [suiteVersion, setSuiteVersion] = createSignal("");
    const [suiteRef, setSuiteRef] = createSignal("");
    const [profileOpen, setProfileOpen] = createSignal(true);
    const [profilePackId, setProfilePackId] = createSignal("acme/extras");
    const [profileName, setProfileName] = createSignal("");
    const onInstallFromGit = vi.fn();
    const onInstallSuite = vi.fn();
    const onApplyProfile = vi.fn();

    render(() => (
      <ExtensionInstallModals
        busy={() => false}
        actionsDisabled={() => false}
        addOpen={addOpen}
        setAddOpen={setAddOpen}
        addUrl={addUrl}
        setAddUrl={setAddUrl}
        addVersion={addVersion}
        setAddVersion={setAddVersion}
        addRef={addRef}
        setAddRef={setAddRef}
        addError={() => undefined}
        onInstallFromGit={onInstallFromGit}
        suiteInstallOpen={suiteOpen}
        setSuiteInstallOpen={setSuiteOpen}
        suiteInstallUrl={suiteUrl}
        setSuiteInstallUrl={setSuiteUrl}
        suiteInstallVersion={suiteVersion}
        setSuiteInstallVersion={setSuiteVersion}
        suiteInstallRef={suiteRef}
        setSuiteInstallRef={setSuiteRef}
        suiteInstallError={() => undefined}
        onInstallSuite={onInstallSuite}
        profileOpen={profileOpen}
        setProfileOpen={setProfileOpen}
        profilePacks={() => [samplePack]}
        profilePackId={profilePackId}
        setProfilePackId={setProfilePackId}
        profileName={profileName}
        setProfileName={setProfileName}
        profileError={() => undefined}
        onApplyProfile={onApplyProfile}
      />
    ));

    fireEvent.input(screen.getByTestId("extensions-install-suite-url"), {
      target: { value: "https://example.com/suite.git" },
    });
    fireEvent.input(screen.getByTestId("extensions-install-suite-version"), {
      target: { value: "^1.2.0" },
    });
    fireEvent.input(screen.getByTestId("extensions-install-suite-ref"), {
      target: { value: "main" },
    });
    expect(suiteUrl()).toBe("https://example.com/suite.git");
    expect(suiteVersion()).toBe("^1.2.0");
    expect(suiteRef()).toBe("main");

    fireEvent.click(screen.getByTestId("extensions-install-suite-submit"));
    expect(onInstallSuite).toHaveBeenCalledOnce();

    fireEvent.click(screen.getByTestId("extensions-add-dialog"));
    expect(addOpen()).toBe(false);

    fireEvent.click(screen.getByTestId("extensions-install-suite-dialog"));
    expect(suiteOpen()).toBe(false);

    fireEvent.click(screen.getByTestId("extensions-profile-dialog"));
    expect(profileOpen()).toBe(false);
  });
});
