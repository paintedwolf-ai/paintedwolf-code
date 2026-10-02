import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { ApprovalsPanel } from "./ApprovalsPanel.tsx";
import {
  createSettingsStore,
  INITIAL_SETTINGS_STATE,
} from "../../../store/settings-store.ts";
import type { ApprovalConfigResponse } from "../../../api/types.ts";

function approvals(
  overrides: Partial<ApprovalConfigResponse> = {},
): ApprovalConfigResponse {
  return {
    scope: "global",
    rules: [{ category: "tool", pattern: "read", effect: "ask" }],
    managed_rules: [],
    merged_from: [],
    ai_rationale_enabled: true,
    never_ask: false,
    ...overrides,
  };
}

function projectApprovals(
  overrides: Partial<ApprovalConfigResponse> = {},
): ApprovalConfigResponse {
  return approvals({
    scope: "project",
    approval_posture: "balanced",
    ai_rationale_enabled: true,
    field_sources: {
      approval_posture: "default",
      ai_rationale_enabled: "default",
      never_ask: "default",
    },
    defaults: {
      approval_posture: "balanced",
      ai_rationale_enabled: true,
      never_ask: false,
    },
    ...overrides,
  });
}

describe("ApprovalsPanel", () => {
  it("pre-selects balanced by default and renders the always-on baseline", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals(),
    });
    const client = stubClient({ updateApprovalsSettings: vi.fn() });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    expect(screen.getByTestId("approvals-settings")).toBeTruthy();
    const balanced = screen.getByTestId(
      "approval-posture-balanced",
    ) as HTMLElement;
    expect(
      (balanced.querySelector("input") as HTMLInputElement).checked,
    ).toBe(true);
    expect(screen.getByTestId("approval-baseline")).toBeTruthy();
    expect(screen.queryByTestId("approval-preview-asks")).toBeNull();
  });

  it("shows extension policy provenance and repository scope", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals({
        managed_rules: [{
          category: "command",
          pattern: "git push*",
          effect: "deny",
          unit_id: "approvals/rules/release",
          pack_id: "acme/policy",
          scope: "project",
        }],
      }),
    });
    const client = stubClient({ updateApprovalsSettings: vi.fn() });

    render(() => (
      <ApprovalsPanel
        client={client}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    const row = screen.getByTestId("approval-managed-rule-approvals/rules/release");
    expect(row.textContent).toContain("git push*");
    expect(row.textContent).toContain("Block");
    expect(row.textContent).toContain("Repository");
    expect(row.textContent).toContain("acme/policy");
  });

  it("persists the chosen posture without sending rules", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals(),
    });
    const updateApprovalsSettings = vi
      .fn()
      .mockResolvedValue(approvals({ approval_posture: "strict" }));
    const client = stubClient({ updateApprovalsSettings });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    fireEvent.click(
      screen
        .getByTestId("approval-posture-strict")
        .querySelector("input") as HTMLInputElement,
    );

    await vi.waitFor(() => {
      expect(updateApprovalsSettings).toHaveBeenCalledWith(
        { approval_posture: "strict" },
        undefined,
      );
      expect(settingsStore.state.approvals?.approval_posture).toBe("strict");
    });
  });

  it("defaults the AI-rationale toggle on and can turn it off", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals(),
    });
    const updateApprovalsSettings = vi
      .fn()
      .mockResolvedValue(approvals({ ai_rationale_enabled: false }));
    const client = stubClient({ updateApprovalsSettings });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    const toggle = screen.getByTestId(
      "approval-ai-rationale-toggle",
    ) as HTMLInputElement;
    expect(toggle.checked).toBe(true);

    fireEvent.click(toggle);

    await vi.waitFor(() => {
      expect(updateApprovalsSettings).toHaveBeenCalledWith(
        { ai_rationale_enabled: false },
        undefined,
      );
      expect(settingsStore.state.approvals?.ai_rationale_enabled).toBe(false);
    });
  });

  it("reflects a persisted disabled AI-rationale setting", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals({ ai_rationale_enabled: false }),
    });
    const client = stubClient({ updateApprovalsSettings: vi.fn() });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    expect(
      (screen.getByTestId("approval-ai-rationale-toggle") as HTMLInputElement)
        .checked,
    ).toBe(false);
  });

  it("reflects a persisted Light posture", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals({ approval_posture: "light" }),
    });
    const client = stubClient({ updateApprovalsSettings: vi.fn() });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    const light = screen.getByTestId("approval-posture-light") as HTMLElement;
    expect((light.querySelector("input") as HTMLInputElement).checked).toBe(true);
  });

  it("switches to Saved approvals and mounts the grants panel", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals(),
    });
    const client = stubClient({
      updateApprovalsSettings: vi.fn(),
      listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }),
    });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    expect(screen.getByTestId("approvals-tab-ask")).toBeTruthy();
    expect(screen.getByTestId("approvals-panel-ask").hidden).toBe(false);
    expect(screen.queryByTestId("saved-approvals-panel")).toBeNull();

    fireEvent.click(screen.getByTestId("approvals-tab-saved"));

    expect(screen.getByTestId("approvals-panel-saved").hidden).toBe(false);
    expect(await screen.findByTestId("saved-approvals-panel")).toBeTruthy();
    expect(screen.getByTestId("saved-approvals-explain").textContent).toMatch(
      /in one place/i,
    );
  });

  it("opens project configuration on Saved approvals with policy one tab away", async () => {
    const client = stubClient({
      updateApprovalsSettings: vi.fn(),
      listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }),
    });
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals(),
    });
    render(() => <ApprovalsPanel client={client} settingsStore={settingsStore}
      alwaysProjectScope projectId="proj-1" />);
    expect(screen.getByTestId("approvals-panel-saved").hidden).toBe(false);
    expect(await screen.findByTestId("saved-approvals-panel")).toBeTruthy();
    fireEvent.click(screen.getByTestId("approvals-tab-ask"));
    expect(screen.getByTestId("project-settings-override")).toBeTruthy();
    expect(screen.queryByTestId("approvals-tab-detections")).toBeNull();
  });

  it("shows the Detections tab at global scope and mounts the panel", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: approvals(),
    });
    const client = stubClient({
      updateApprovalsSettings: vi.fn(),
      listDetectionPacks: vi.fn().mockResolvedValue({ packs: [], rejected: [] }),
    });

    render(() => (
      <ApprovalsPanel client={client} settingsStore={settingsStore} />
    ));

    expect(screen.getByTestId("approvals-tab-detections")).toBeTruthy();
    fireEvent.click(screen.getByTestId("approvals-tab-detections"));

    expect(screen.getByTestId("approvals-panel-detections").hidden).toBe(
      false,
    );
    expect(await screen.findByTestId("detections-panel")).toBeTruthy();
  });

  it("project scope starts following Settings with a main override control", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals(),
    });
    const updateApprovalsSettings = vi.fn().mockResolvedValue(
      projectApprovals({
        approval_posture: "balanced",
        field_sources: {
          approval_posture: "override",
          ai_rationale_enabled: "override",
          never_ask: "default",
        },
      }),
    );
    const client = stubClient({ updateApprovalsSettings, listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }) });

    render(() => (
      <ApprovalsPanel
        client={client}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    expect(screen.getByTestId("approvals-settings").getAttribute("data-scope")).toBe(
      "project",
    );
    expect(screen.getByTestId("settings-scope-badge").textContent).toBe("Project");
    expect(screen.getByTestId("settings-scope-lede").getAttribute("data-scope")).toBe(
      "project",
    );
    expect(screen.getByTestId("settings-scope-lede").textContent).toMatch(
      /approval cards/,
    );
    expect(screen.getByTestId("approvals-tab-saved")).toBeTruthy();
    expect(screen.getByTestId("approvals-panel-ask").hidden).toBe(false);
    expect(screen.queryByTestId("approvals-tab-detections")).toBeNull();
    expect(screen.queryByTestId("approval-baseline")).toBeNull();
    expect(screen.queryByTestId("approval-posture-balanced")).toBeNull();

    expect(screen.getByTestId("project-settings-override").getAttribute("data-enabled")).toBe(
      "false",
    );
    expect(screen.getByTestId("project-override-summary").textContent).toMatch(
      /Following Settings · Balanced/,
    );

    fireEvent.click(screen.getByTestId("project-override-toggle"));

    await vi.waitFor(() => {
      expect(updateApprovalsSettings).toHaveBeenCalledWith(
        {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
        },
        "proj-1",
      );
    });
  });

  it("disabling the project override clears all approval overlay fields", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals({
        field_sources: {
          approval_posture: "override",
          ai_rationale_enabled: "override",
          never_ask: "override",
        },
        approval_posture: "strict",
        ai_rationale_enabled: false,
      }),
    });
    const updateApprovalsSettings = vi.fn().mockResolvedValue(projectApprovals());
    const client = stubClient({ updateApprovalsSettings });

    render(() => (
      <ApprovalsPanel
        client={client}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    expect(screen.getByTestId("project-settings-override").getAttribute("data-enabled")).toBe(
      "true",
    );
    expect(screen.getByTestId("approval-posture-strict")).toBeTruthy();
    expect(screen.getByTestId("approval-baseline")).toBeTruthy();
    expect(
      screen.getByTestId("approval-posture-strict").textContent,
    ).toMatch(/Balanced, plus/);
    expect(
      (screen
        .getByTestId("approval-posture-strict")
        .querySelector("input") as HTMLInputElement).checked,
    ).toBe(true);

    fireEvent.click(screen.getByTestId("project-override-toggle"));

    await vi.waitFor(() => {
      expect(updateApprovalsSettings).toHaveBeenCalledWith(
        {
          approval_posture: null,
          ai_rationale_enabled: null,
          never_ask: null,
        },
        "proj-1",
      );
    });
  });

  it("shows only project postures that are at least as strict as Settings", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals({
        approval_posture: "strict",
        field_sources: {
          approval_posture: "override",
          ai_rationale_enabled: "default",
          never_ask: "default",
        },
        defaults: {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
          never_ask: false,
        },
      }),
    });
    render(() => (
      <ApprovalsPanel
        client={stubClient({ updateApprovalsSettings: vi.fn() })}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    expect(screen.queryByTestId("approval-posture-light")).toBeNull();
    expect(screen.getByTestId("approval-posture-balanced")).toBeTruthy();
    expect(screen.getByTestId("approval-posture-strict")).toBeTruthy();
  });

  it("restores approvals for one project when device approvals are Off", async () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals({
        never_ask: true,
        defaults: {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
          never_ask: true,
        },
      }),
    });
    const updateApprovalsSettings = vi.fn().mockResolvedValue(
      projectApprovals({
        never_ask: false,
        field_sources: {
          approval_posture: "default",
          ai_rationale_enabled: "default",
          never_ask: "override",
        },
        defaults: {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
          never_ask: true,
        },
      }),
    );
    render(() => (
      <ApprovalsPanel
        client={stubClient({ updateApprovalsSettings })}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    expect(screen.getByTestId("project-override-summary").textContent).toMatch(/Off/);
    fireEvent.click(screen.getByTestId("project-override-toggle"));
    await vi.waitFor(() => {
      expect(updateApprovalsSettings).toHaveBeenCalledWith(
        {
          approval_posture: "balanced",
          ai_rationale_enabled: true,
          never_ask: false,
        },
        "proj-1",
      );
    });
  });
  it("reports project approvals it could not apply and locks editing until the file is fixed", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals({
        approval_posture: "strict",
        field_sources: {
          approval_posture: "override",
          ai_rationale_enabled: "default",
          never_ask: "default",
        },
        rejected: [
          {
            entry: "approval_posture",
            code: "invalid_entry",
            detail: 'unknown approval posture "stirct" (use light, balanced, or strict); this project uses strict until it is fixed',
          },
          { code: "project_unreadable", detail: "could not parse .paintedwolf/approvals.yaml" },
        ],
      }),
    });
    const client = stubClient({ listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }) });

    render(() => (
      <ApprovalsPanel
        client={client}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    const section = screen.getByTestId("approval-rejected");
    expect(section.getAttribute("role")).toBe("alert");
    expect(screen.getByTestId("approval-rejected-row-0").getAttribute("data-code")).toBe("invalid_entry");
    expect(screen.getByTestId("approval-rejected-row-0").textContent).toMatch(/approval_posture/);
    expect(screen.getByTestId("approval-rejected-row-0").textContent).toMatch(/stirct/);
    expect(screen.getByTestId("approval-rejected-row-1").textContent).toMatch(/The whole file/);
    expect(screen.getByTestId("approval-posture-strict").closest("fieldset")?.disabled).toBe(true);
  });

  it("shows no repair notice when the project file applied cleanly", () => {
    const settingsStore = createSettingsStore({
      ...INITIAL_SETTINGS_STATE,
      approvals: projectApprovals(),
    });
    const client = stubClient({ listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }) });

    render(() => (
      <ApprovalsPanel
        client={client}
        settingsStore={settingsStore}
        alwaysProjectScope
        initialTab="ask"
        projectId="proj-1"
      />
    ));

    expect(screen.queryByTestId("approval-rejected")).toBeNull();
  });
});
