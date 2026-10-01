import {
  cleanup,
  findByTestId,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type {
  NativeUpdateState,
  UpdateService,
} from "../../../settings/system/update-service.ts";
import { UpdatesSettingsPanel } from "./UpdatesSettingsPanel.tsx";

const direct = (over: Partial<NativeUpdateState> = {}): NativeUpdateState => ({
  phase: "idle",
  revision: 0,
  current_version: "0.1.0",
  channel: "stable",
  rollout_eligibility: "not_applicable",
  install_source: "direct_download",
  checks_enabled: true,
  downloaded_bytes: 0,
  total_bytes: null,
  ...over,
});

function service(over: Partial<UpdateService> = {}): UpdateService {
  return {
    getState: vi.fn(async () => direct()),
    setChecksEnabled: vi.fn(async (enabled) =>
      direct({ checks_enabled: enabled })),
    setChannel: vi.fn(async (channel) => direct({ channel })),
    check: vi.fn(async () => direct({ phase: "up_to_date" })),
    install: vi.fn(async () => direct({ phase: "restart_required" })),
    subscribe: vi.fn(async () => () => {}),
    ...over,
  };
}

describe("UpdatesSettingsPanel", () => {
  afterEach(cleanup);

  it("does not invoke the native updater in the web app", () => {
    render(() => <UpdatesSettingsPanel />);
    expect(screen.getByTestId("updates-unavailable").textContent).toContain("desktop app");
    expect(screen.queryByTestId("updates-check-now")).toBeNull();
    expect(screen.queryByTestId("updates-install-status")).toBeNull();
  });

  it("explains the automatic launch and periodic check cadence", async () => {
    const rendered = render(() => <UpdatesSettingsPanel updateService={service()} />);
    await waitFor(() =>
      expect(rendered.baseElement.textContent).toMatch(/shortly after launch and every six hours/i),
    );
    expect(rendered.baseElement.textContent).toMatch(
      /reach devices gradually.*Check now always offers the newest/i,
    );
  });

  it("renders the host-managed check result", async () => {
    const updates = service();
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(rendered.getByTestId("updates-check-now")).not.toHaveProperty(
        "disabled",
        true,
      ),
    );
    rendered.getByTestId("updates-check-now").click();
    await findByTestId(rendered.baseElement as HTMLElement, "updates-status");
    await waitFor(() =>
      expect(rendered.getByTestId("updates-status").textContent).toMatch(
        /latest version/i,
      ),
    );
    expect(updates.check).toHaveBeenCalledTimes(1);
  });

  it("persists a channel switch and clears the prior candidate", async () => {
    const updates = service({
      getState: vi.fn(async () =>
        direct({ phase: "available", available_version: "1.0.0-rc.2" }),
      ),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(rendered.getByTestId("updates-channel")).not.toHaveProperty(
        "disabled",
        true,
      ),
    );
    fireEvent.click(rendered.getByTestId("updates-channel"));
    fireEvent.click(screen.getByRole("option", { name: /Preview/ }));
    await waitFor(() => expect(updates.setChannel).toHaveBeenCalledWith("preview"));
    expect(rendered.queryByText("1.0.0-rc.2")).toBeNull();
  });

  it("distinguishes a held-back rollout from an up-to-date device", async () => {
    const updates = service({
      getState: vi.fn(async () =>
        direct({
          phase: "held_back",
          available_version: "0.2.0",
          rollout_eligibility: "held_back",
        }),
      ),
    });
    const rendered = render(() => <UpdatesSettingsPanel updateService={updates} />);

    await waitFor(() =>
      expect(rendered.getByTestId("updates-status").textContent).toMatch(
        /not available to this device yet/i,
      ),
    );
    expect(rendered.getByTestId("updates-current-version").textContent).toContain(
      "0.1.0",
    );
    expect(rendered.getByTestId("updates-rollout-eligibility").textContent).toMatch(
      /waiting/i,
    );
  });

  it("renders automatic check results delivered after mount", async () => {
    let deliver: ((state: NativeUpdateState) => void) | undefined;
    const updates = service({
      subscribe: vi.fn(async (handler) => {
        deliver = handler;
        return () => {};
      }),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() => expect(deliver).toBeTypeOf("function"));
    deliver?.(direct({ revision: 1, phase: "available", available_version: "0.2.0" }));
    await waitFor(() =>
      expect(rendered.getByTestId("updates-status").textContent).toContain("0.2.0"),
    );
  });

  it("reports an unavailable update event stream", async () => {
    const updates = service({
      subscribe: vi.fn(async () => {
        throw new Error("update_event_stream_unavailable");
      }),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));

    await waitFor(() =>
      expect(rendered.getByTestId("updates-install-status").textContent).toContain(
        "update_event_stream_unavailable",
      ),
    );
  });

  it("installs only the version retained by the native service", async () => {
    const updates = service({
      check: vi.fn(async () =>
        direct({ phase: "available", available_version: "0.2.0" }),
      ),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect((rendered.getByTestId("updates-check-now") as HTMLButtonElement).disabled)
        .toBe(false),
    );
    rendered.getByTestId("updates-check-now").click();
    await findByTestId(rendered.baseElement as HTMLElement, "updates-install");
    rendered.getByTestId("updates-install").click();
    await waitFor(() => expect(updates.install).toHaveBeenCalledWith("0.2.0"));
  });

  it("shows release notes for the retained candidate", async () => {
    const updates = service({
      getState: vi.fn(async () =>
        direct({
          phase: "available",
          available_version: "0.2.0",
          notes: "Reliability and performance improvements.",
        }),
      ),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(rendered.getByTestId("updates-release-notes").textContent).toContain(
        "Reliability and performance improvements.",
      ),
    );
  });

  it("recovers the available state when the native install command is refused", async () => {
    const updates = service({
      getState: vi.fn(async () => direct({ phase: "available", available_version: "0.2.0" })),
      install: vi.fn().mockRejectedValue({ code: "candidate_changed" }),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect((rendered.getByTestId("updates-install") as HTMLButtonElement).disabled)
        .toBe(false),
    );
    rendered.getByTestId("updates-install").click();
    await findByTestId(
      rendered.baseElement as HTMLElement,
      "updates-install-status",
    );
    expect((rendered.getByTestId("updates-install") as HTMLButtonElement).disabled)
      .toBe(false);
  });

  it("routes a package-managed update to Homebrew", async () => {
    const updates = service({
      check: vi.fn(async (): Promise<NativeUpdateState> => ({
        ...direct({ phase: "available", available_version: "0.2.0" }),
        install_source: "homebrew_cask",
      })),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect((rendered.getByTestId("updates-check-now") as HTMLButtonElement).disabled)
        .toBe(false),
    );
    rendered.getByTestId("updates-check-now").click();
    const brew = await findByTestId(
      rendered.baseElement as HTMLElement,
      "updates-brew-upgrade",
    );
    expect(brew.textContent).toContain("brew upgrade");
    expect(rendered.baseElement.querySelector('[data-testid="updates-install"]')).toBeNull();
  });

  it("routes a preview cask update to the preview token", async () => {
    const updates = service({
      getState: vi.fn(async () =>
        direct({
          phase: "available",
          available_version: "1.0.0-rc.2",
          channel: "preview",
          install_source: "homebrew_cask",
        }),
      ),
    });
    const rendered = render(() => <UpdatesSettingsPanel updateService={updates} />);
    await waitFor(() =>
      expect(rendered.getByTestId("updates-brew-upgrade").textContent).toContain(
        "painted-wolf-code@preview",
      ),
    );
  });

  it("keeps explicit checks available after automatic opt-out", async () => {
    const updates = service();
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect((rendered.getByTestId("updates-check-enabled") as HTMLInputElement).disabled)
        .toBe(false),
    );
    rendered.getByTestId("updates-check-enabled").click();
    await waitFor(() =>
      expect(updates.setChecksEnabled).toHaveBeenCalledWith(false),
    );
    expect(updates.check).not.toHaveBeenCalled();
    await waitFor(() => expect((rendered.getByTestId("updates-check-now") as HTMLButtonElement).disabled)
      .toBe(false));
    rendered.getByTestId("updates-check-now").click();
    await waitFor(() => expect(updates.check).toHaveBeenCalledTimes(1));
  });

  it("renders the host-managed opt-out after remount", async () => {
    const updates = service({
      getState: vi.fn(async () => direct({ checks_enabled: false })),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(
        (rendered.getByTestId("updates-check-enabled") as HTMLInputElement)
          .checked,
      ).toBe(false),
    );
    expect(
      (rendered.getByTestId("updates-check-now") as HTMLButtonElement).disabled,
    ).toBe(false);
  });

  it("restores the toggle when persistence fails", async () => {
    const updates = service({
      setChecksEnabled: vi.fn().mockRejectedValue({ code: "preferences_unavailable", detail: "write failed" }),
    });
    const rendered = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(
        (rendered.getByTestId("updates-check-enabled") as HTMLInputElement)
          .disabled,
      ).toBe(false),
    );
    rendered.getByTestId("updates-check-enabled").click();
    await waitFor(() =>
      expect(
        (rendered.getByTestId("updates-check-enabled") as HTMLInputElement)
          .checked,
      ).toBe(true),
    );
  });

  it("keeps a newer native preference when the command reply and recovery read fail", async () => {
    let deliver: ((state: NativeUpdateState) => void) | undefined;
    const getState = vi.fn().mockResolvedValueOnce(direct({ revision: 1 }))
      .mockRejectedValue(new Error("connection closed"));
    const updates = service({
      getState,
      subscribe: async (handler) => { deliver = handler; return () => {}; },
      setChecksEnabled: async () => {
        deliver?.(direct({ revision: 2, checks_enabled: false }));
        throw new Error("connection closed");
      },
    });
    render(() => <UpdatesSettingsPanel updateService={updates} />);
    const toggle = screen.getByTestId("updates-check-enabled");
    await waitFor(() => expect(toggle).toHaveProperty("disabled", false));
    fireEvent.click(toggle);
    await waitFor(() => expect(getState).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(toggle).toHaveProperty("disabled", false));
    expect(toggle).toHaveProperty("checked", false);
  });

  it("restores a restart-required journal after remount", async () => {
    const updates = service({
      getState: vi.fn(async () => direct({
        phase: "restart_required",
        available_version: "0.2.0",
      })),
    });
    const first = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(first.getByTestId("updates-install").textContent).toContain("Restart now"),
    );
    first.unmount();
    const second = render(() => (
      <UpdatesSettingsPanel updateService={updates} />
    ));
    await waitFor(() =>
      expect(second.getByTestId("updates-install").textContent).toContain("Restart now"),
    );
    expect(updates.getState).toHaveBeenCalledTimes(2);
    expect(second.getByTestId("updates-channel")).toHaveProperty("disabled", true);
    expect(second.getByTestId("updates-check-now")).toHaveProperty("disabled", true);
    await waitFor(() => expect(second.getByTestId("updates-install")).toHaveProperty("disabled", false));
    fireEvent.click(second.getByTestId("updates-install"));
    await waitFor(() => expect(updates.install).toHaveBeenCalledWith("0.2.0"));
  });
  it("explains interrupted installation without exposing a raw error code", async () => {
    render(() => <UpdatesSettingsPanel updateService={service({
      getState: async () => direct({ error: { code: "interrupted" } }),
    })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("Check for updates to try again");
    expect(screen.getByTestId("updates-install-status").textContent).not.toContain("update_install_interrupted");
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", false);
  });

  it("keeps technical diagnostics separate from recovery guidance", async () => {
    render(() => <UpdatesSettingsPanel updateService={service({
      getState: async () => direct({ error: { code: "install_failed", detail: "/private/test/app: disk full" } }),
    })} />);
    expect((await screen.findByRole("alert")).textContent).toContain("could not be installed");
    expect(screen.getByRole("alert").textContent).not.toContain("/private/test/app");
    expect(screen.getByText("Technical details").closest("details")?.hasAttribute("open")).toBe(false);
  });

  it("does not mislabel an unreadable installation receipt as Homebrew", async () => {
    render(() => <UpdatesSettingsPanel updateService={service({
      getState: async () => direct({ phase: "available", available_version: "0.2.0",
        install_source: "unknown", error: { code: "install_source_unavailable" } }),
    })} />);
    await screen.findByRole("alert");
    expect(screen.queryByTestId("updates-brew-upgrade")).toBeNull();
    expect(screen.queryByTestId("updates-install")).toBeNull();
  });

  it("can finish an installed update when its installation source is unknown", async () => {
    const updates = service({
      getState: async () => direct({ phase: "restart_required", available_version: "0.2.0",
        install_source: "unknown" }),
    });
    render(() => <UpdatesSettingsPanel updateService={updates} />);
    const restart = await screen.findByRole("button", { name: "Restart now" });
    await waitFor(() => expect(restart).toHaveProperty("disabled", false));
    fireEvent.click(restart);
    await waitFor(() => expect(updates.install).toHaveBeenCalledWith("0.2.0"));
  });

  it("ignores an older snapshot arriving after a newer native event", async () => {
    let deliver: ((state: NativeUpdateState) => void) | undefined;
    let resolveRead: ((state: NativeUpdateState) => void) | undefined;
    const updates = service({
      subscribe: async (handler) => { deliver = handler; return () => {}; },
      getState: () => new Promise((resolve) => { resolveRead = resolve; }),
    });
    render(() => <UpdatesSettingsPanel updateService={updates} />);
    await waitFor(() => expect(resolveRead).toBeTypeOf("function"));
    deliver?.(direct({ revision: 3, phase: "restart_required", available_version: "0.2.0" }));
    resolveRead?.(direct({ revision: 2, phase: "available", available_version: "0.2.0" }));
    await waitFor(() => expect(screen.getByTestId("updates-install").textContent).toContain("Restart now"));
    expect(screen.getByTestId("updates-check-now")).toHaveProperty("disabled", true);
    deliver?.(direct({ revision: 1 }));
    expect(screen.getByTestId("updates-install").textContent).toContain("Restart now");
  });

  it("keeps a completed native installation when its command reply is lost", async () => {
    let deliver: ((state: NativeUpdateState) => void) | undefined;
    const getState = vi.fn().mockResolvedValueOnce(direct({
      revision: 1, phase: "available", available_version: "0.2.0",
    })).mockRejectedValue(new Error("connection closed"));
    const updates = service({
      getState,
      subscribe: async (handler) => { deliver = handler; return () => {}; },
      install: async () => {
        deliver?.(direct({ revision: 2, phase: "restart_required", available_version: "0.2.0" }));
        throw new Error("connection closed");
      },
    });
    render(() => <UpdatesSettingsPanel updateService={updates} />);
    const install = await screen.findByTestId("updates-install");
    await waitFor(() => expect(install).toHaveProperty("disabled", false));
    fireEvent.click(install);
    await waitFor(() => expect(getState).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(screen.getByTestId("updates-install").textContent).toContain("Restart now");
  });

});
