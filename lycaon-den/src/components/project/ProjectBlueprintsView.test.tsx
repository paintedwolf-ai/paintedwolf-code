import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BlueprintSummary, WorkflowSummary } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { createAppStore } from "../../store/app-state.ts";
import {
  clearPendingArmWorkflow,
  peekArmWorkflow,
} from "../../chat/workflow/pending-arm.ts";
import { ProjectBlueprintsView } from "./ProjectBlueprintsView.tsx";
import {
  blueprintKindLabel,
  blueprintListTitle,
  blueprintPathLabel,
  clampBlueprintList,
  defaultCompatibleWorkflow,
  PROJECT_BLUEPRINTS_LIST_CAP,
  sortBlueprintSummaries,
  workflowPickerLabel,
} from "./project-blueprints-model.ts";

const listBlueprints = vi.fn();
const launchBlueprint = vi.fn();
const listWorkflows = vi.fn();
const createSupportingSession = vi.fn();
const updateBlueprint = vi.fn();
const deleteBlueprint = vi.fn();

const mockClient = {
  listBlueprints,
  launchBlueprint,
  listWorkflows,
  updateBlueprint,
  deleteBlueprint,
};

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return {
    ...actual,
    getLycaonClient: () => mockClient,
  };
});

function fixtureRows(): BlueprintSummary[] {
  return [
    {
      id: "bp-1",
      title: "Ship auth",
      path: ".paintedwolf/blueprints/plan.md",
      status: "draft",
      version: 1,
      compatible_workflows: ["options", "plan"],
      updated_at: "2026-07-01T12:00:00Z",
    },
    {
      id: "bp-2",
      title: "Threat model",
      path: ".paintedwolf/blueprints/threat-model.md",
      status: "approved",
      version: 2,
      compatible_workflows: ["threat-model"],
      updated_at: "2026-07-02T12:00:00Z",
    },
  ];
}

/** Three rows so a batch can land, fail, and land again around one refusal. */
function threeRows(): BlueprintSummary[] {
  return [
    ...fixtureRows(),
    {
      id: "bp-3",
      title: "Rollout",
      path: ".paintedwolf/blueprints/rollout.md",
      status: "draft",
      version: 1,
      compatible_workflows: ["plan"],
      updated_at: "2026-07-03T12:00:00Z",
    },
  ];
}

const catalog: WorkflowSummary[] = [
  {
    id: "plan",
    version: "1.0.0",
    name: "Plan",
    icon: "route",
    featured: true,
    supports_blueprints: true,
    trigger: "/plan",
    description: "Research, expand, approve, then build.",
  },
  {
    id: "options",
    version: "1.0.0",
    name: "Decide",
    supports_blueprints: true,
    trigger: "/options",
    description: "Clarify the decision, then debate it.",
  },
  {
    id: "threat-model",
    version: "1.0.0",
    name: "Threat model",
    trigger: "/threat-model",
  },
  {
    id: "recon-pack",
    version: "1.0.0",
    name: "Recon",
    featured: true,
    requires_repo: true,
  },
];

describe("project-blueprints-model", () => {
  it("prefers plan when listed among compatible workflows", () => {
    expect(defaultCompatibleWorkflow(["options", "plan"])).toBe("plan");
    expect(defaultCompatibleWorkflow(["threat-model"])).toBe("threat-model");
    expect(defaultCompatibleWorkflow([])).toBeUndefined();
  });

  it("labels kind from compatible workflows or basename", () => {
    expect(
      blueprintKindLabel({
        ...fixtureRows()[0]!,
        compatible_workflows: ["plan"],
      }),
    ).toBe("Plan");
    expect(blueprintKindLabel(fixtureRows()[0]!)).toBe("plan.md");
    expect(blueprintKindLabel(fixtureRows()[1]!)).toBe("threat-model.md");
    expect(blueprintListTitle({ ...fixtureRows()[0]!, title: "  " })).toBe(
      "Untitled blueprint",
    );
  });

  it("clamps to host cap and labels workflows", () => {
    const many = Array.from({ length: PROJECT_BLUEPRINTS_LIST_CAP + 5 }, (_, i) => ({
      ...fixtureRows()[0]!,
      path: `.paintedwolf/blueprints/bp-${i}.md`,
      title: `Ship auth ${i}`,
    }));
    expect(clampBlueprintList(many)).toHaveLength(PROJECT_BLUEPRINTS_LIST_CAP);
    expect(workflowPickerLabel("plan", [])).toBe("Plan");
    expect(
      workflowPickerLabel("plan", [{ id: "plan", version: "1", name: "Plan workflow" }]),
    ).toBe("Plan workflow");
  });

  it("labels path basename and sorts by title / updated", () => {
    expect(blueprintPathLabel(fixtureRows()[0]!)).toBe("plan.md");
    const byTitle = sortBlueprintSummaries(fixtureRows(), "title", "asc");
    expect(byTitle.map((r) => r.title)).toEqual(["Ship auth", "Threat model"]);
    const byUpdated = sortBlueprintSummaries(fixtureRows(), "updated", "desc");
    expect(byUpdated.map((r) => r.title)).toEqual(["Threat model", "Ship auth"]);
  });
});

describe("ProjectBlueprintsView", () => {
  beforeEach(() => {
    listBlueprints.mockReset();
    launchBlueprint.mockReset();
    listWorkflows.mockReset();
    createSupportingSession.mockReset();
    createSupportingSession.mockResolvedValue(undefined);
    updateBlueprint.mockReset();
    deleteBlueprint.mockReset();
    listWorkflows.mockResolvedValue(catalog);
    clearPendingArmWorkflow();
    vi.stubGlobal("confirm", vi.fn(() => true));
  });

  afterEach(() => {
    clearPendingArmWorkflow();
    vi.unstubAllGlobals();
  });

  it("renders empty state with blueprint workflow launcher", async () => {
    listBlueprints.mockResolvedValue([]);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    expect(await screen.findByTestId("project-blueprints-empty")).toBeTruthy();
    expect(screen.getByText(/No blueprints yet/i)).toBeTruthy();
    expect(
      await screen.findByTestId("project-blueprints-workflow-launcher"),
    ).toBeTruthy();
    expect(screen.getByTestId("session-launcher-tile-plan")).toBeTruthy();
    expect(screen.getByTestId("session-launcher-tile-options")).toBeTruthy();
    expect(screen.queryByTestId("session-launcher-tile-recon-pack")).toBeNull();
    expect(screen.queryByTestId("session-launcher-more")).toBeNull();
  });

  it("waits for one coherent list and launcher projection", async () => {
    let resolveBlueprints!: (value: BlueprintSummary[]) => void;
    let resolveWorkflows!: (value: WorkflowSummary[]) => void;
    listBlueprints.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveBlueprints = resolve;
      }),
    );
    listWorkflows.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveWorkflows = resolve;
      }),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));

    expect(screen.queryByTestId("project-blueprints-loading")).toBeNull();
    expect(screen.queryByTestId("project-blueprints-empty")).toBeNull();
    resolveBlueprints([]);
    await waitFor(() => {
      expect(listBlueprints).toHaveBeenCalledWith("proj-1");
    });
    expect(screen.queryByTestId("project-blueprints-empty")).toBeNull();
    expect(screen.queryByTestId("project-blueprints-launcher")).toBeNull();

    resolveWorkflows(catalog);
    expect(await screen.findByTestId("project-blueprints-empty")).toBeTruthy();
    expect(screen.getByTestId("project-blueprints-launcher")).toBeTruthy();
    expect(screen.queryByTestId("project-blueprints-loading")).toBeNull();
  });

  it("shows launcher above an existing blueprint list", async () => {
    listBlueprints.mockResolvedValue(fixtureRows());
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    expect(await screen.findByTestId("project-blueprints-list")).toBeTruthy();
    expect(
      screen.getByTestId("project-blueprints-workflow-launcher"),
    ).toBeTruthy();
    expect(screen.getByTestId("session-launcher-tile-plan")).toBeTruthy();
  });

  it("opens an in-stage More list when blueprint workflows exceed the strip", async () => {
    const many: WorkflowSummary[] = Array.from({ length: 6 }, (_, i) => ({
      id: `bp-${i}`,
      version: "1.0.0",
      name: `Blueprint ${i}`,
      supports_blueprints: true as const,
      featured: i < 2,
    }));
    listBlueprints.mockResolvedValue([]);
    listWorkflows.mockResolvedValue(many);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    expect(
      await screen.findByTestId("project-blueprints-workflow-launcher"),
    ).toBeTruthy();
    expect(screen.getByTestId("session-launcher-more")).toBeTruthy();
    expect(screen.queryByTestId("session-launcher-tile-bp-4")).toBeNull();
    fireEvent.click(screen.getByTestId("session-launcher-more"));
    expect(
      await screen.findByTestId("project-blueprints-launcher-more"),
    ).toBeTruthy();
    fireEvent.click(screen.getByTestId("project-blueprints-more-bp-4"));
    await waitFor(() => {
      expect(createSupportingSession).toHaveBeenCalledWith(
        expect.objectContaining({ id: "bp-4" }),
      );
    });
  });

  it("arms a supporting workflow from a launcher tile", async () => {
    listBlueprints.mockResolvedValue([]);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    fireEvent.click(await screen.findByTestId("session-launcher-tile-plan"));
    await waitFor(() => {
      expect(createSupportingSession).toHaveBeenCalledWith(
        expect.objectContaining({ id: "plan", trigger: "/plan" }),
      );
    });
  });

  it("lists blueprints and opens compatible picker with plan preselected", async () => {
    listBlueprints.mockResolvedValue(fixtureRows());
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    expect(await screen.findByTestId("project-blueprints-list")).toBeTruthy();
    expect(screen.getByText("Ship auth")).toBeTruthy();
    expect(screen.getByText("Threat model")).toBeTruthy();

    const shipRow = screen
      .getAllByTestId("project-blueprint-row")
      .find(
        (el) =>
          el.getAttribute("data-blueprint-path") ===
          ".paintedwolf/blueprints/plan.md",
      )!;
    fireEvent.click(shipRow.querySelector('[data-testid="project-blueprint-run"]')!);
    const picker = await screen.findByTestId("project-blueprint-workflow-picker");
    fireEvent.click(picker);
    const selected = screen
      .getAllByRole("option")
      .find((option) => option.getAttribute("aria-selected") === "true");
    expect(selected?.getAttribute("data-value")).toBe("plan");
  });

  it("defers start and arms when running a single-compatible blueprint", async () => {
    listBlueprints.mockResolvedValue(fixtureRows());
    launchBlueprint.mockResolvedValue({
      session_id: "sess-new",
      blueprint_path: ".paintedwolf/blueprints/threat-model-launched.md",
    });
    const onLaunchedSession = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onLaunchedSession={onLaunchedSession}
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    const threatRow = screen
      .getAllByTestId("project-blueprint-row")
      .find(
        (el) =>
          el.getAttribute("data-blueprint-path") ===
          ".paintedwolf/blueprints/threat-model.md",
      )!;
    fireEvent.click(threatRow.querySelector('[data-testid="project-blueprint-run"]')!);
    await waitFor(() => {
      expect(launchBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-2",
        {
          target_workflow_id: "threat-model",
          defer_start: true,
        },
      );
      expect(onLaunchedSession).toHaveBeenCalledWith({ sessionId: "sess-new" });
      expect(peekArmWorkflow("sess-new")).toEqual(
        expect.objectContaining({ id: "threat-model" }),
      );
    });
  });

  it("defers start and arms from picker Start button", async () => {
    listBlueprints.mockResolvedValue([fixtureRows()[0]!]);
    launchBlueprint.mockResolvedValue({
      session_id: "sess-plan",
      blueprint_path: ".paintedwolf/blueprints/plan-launched.md",
    });
    const onLaunchedSession = vi.fn();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onLaunchedSession={onLaunchedSession}
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    fireEvent.click(screen.getByTestId("project-blueprint-run"));
    await screen.findByTestId("project-blueprint-workflow-picker");
    fireEvent.click(screen.getByTestId("project-blueprint-launch"));
    await waitFor(() => {
      expect(launchBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-1",
        {
          target_workflow_id: "plan",
          defer_start: true,
        },
      );
      expect(onLaunchedSession).toHaveBeenCalledWith({ sessionId: "sess-plan" });
      expect(peekArmWorkflow("sess-plan")).toEqual(
        expect.objectContaining({ id: "plan", trigger: "/plan" }),
      );
    });
  });

  it("renames a blueprint title via the row menu", async () => {
    listBlueprints
      .mockResolvedValueOnce(fixtureRows())
      .mockResolvedValueOnce([
        { ...fixtureRows()[0]!, title: "Auth ship" },
        fixtureRows()[1]!,
      ]);
    updateBlueprint.mockResolvedValue({
      ...fixtureRows()[0]!,
      title: "Auth ship",
      content: "---\ntitle: Auth ship\n---\n",
      project_id: "proj-1",
    });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    await screen.findByTestId("project-blueprints-list");
    const shipRow = screen
      .getAllByTestId("project-blueprint-row")
      .find(
        (el) =>
          el.getAttribute("data-blueprint-path") ===
          ".paintedwolf/blueprints/plan.md",
      )!;
    fireEvent.click(shipRow.querySelector('[data-testid="project-blueprint-menu"]')!);
    fireEvent.click(screen.getByTestId("project-blueprint-rename"));
    const input = await screen.findByTestId("blueprint-rename-input");
    fireEvent.input(input, { target: { value: "Auth ship" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => {
      expect(updateBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-1",
        { title: "Auth ship" },
      );
    });
    expect(await screen.findByText("Auth ship")).toBeTruthy();
  });

  it("deletes a blueprint after confirm", async () => {
    listBlueprints
      .mockResolvedValueOnce(fixtureRows())
      .mockResolvedValueOnce([fixtureRows()[1]!]);
    deleteBlueprint.mockResolvedValue(undefined);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    await screen.findByTestId("project-blueprints-list");
    const shipRow = screen
      .getAllByTestId("project-blueprint-row")
      .find(
        (el) =>
          el.getAttribute("data-blueprint-path") ===
          ".paintedwolf/blueprints/plan.md",
      )!;
    fireEvent.click(shipRow.querySelector('[data-testid="project-blueprint-menu"]')!);
    fireEvent.click(screen.getByTestId("project-blueprint-delete"));
    await waitFor(() => {
      expect(window.confirm).toHaveBeenCalled();
      expect(deleteBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-1",
      );
    });
    await waitFor(() => {
      expect(screen.queryByText("Ship auth")).toBeNull();
      expect(screen.getByText("Threat model")).toBeTruthy();
    });
  });

  it("multi-selects and bulk-deletes blueprints", async () => {
    listBlueprints
      .mockResolvedValueOnce(fixtureRows())
      .mockResolvedValueOnce([]);
    deleteBlueprint.mockResolvedValue(undefined);
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    await screen.findByTestId("project-blueprints-list");
    expect(screen.queryByTestId("project-blueprints-toolbar")).toBeNull();

    const checks = screen.getAllByTestId("project-blueprint-select");
    fireEvent.click(checks[0]!);
    expect(await screen.findByTestId("project-blueprints-toolbar")).toBeTruthy();
    fireEvent.click(checks[1]!);
    fireEvent.click(screen.getByTestId("project-blueprints-bulk-delete"));

    await waitFor(() => {
      expect(window.confirm).toHaveBeenCalled();
      expect(deleteBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-2",
      );
      expect(deleteBlueprint).toHaveBeenCalledWith(
        "proj-1",
        "bp-1",
      );
    });
    await waitFor(() => {
      expect(screen.queryByTestId("project-blueprints-toolbar")).toBeNull();
      expect(screen.getByTestId("project-blueprints-empty")).toBeTruthy();
    });
  });

  it("finishes a bulk delete past a refusal and says which row survived", async () => {
    const rows = threeRows();
    const refused = rows[1]!.id;
    listBlueprints
      .mockResolvedValueOnce(rows)
      .mockResolvedValueOnce([rows[1]!]);
    deleteBlueprint.mockImplementation(async (_projectId: string, blueprintId: string) => {
      if (blueprintId === refused) {
        throw new LycaonApiError("Permission denied.", 500, "internal_error");
      }
    });
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    fireEvent.click(screen.getByTestId("project-blueprints-select-all"));
    fireEvent.click(screen.getByTestId("project-blueprints-bulk-delete"));

    await waitFor(() => {
      expect(deleteBlueprint).toHaveBeenCalledTimes(3);
    });
    for (const row of rows) {
      expect(deleteBlueprint).toHaveBeenCalledWith("proj-1", row.id);
    }

    const banner = await screen.findByTestId("project-blueprints-error");
    expect(banner.textContent).toBe(
      "Deleted 2 of 3. “Threat model” could not be deleted — Permission denied.",
    );
    const failure = await screen.findByTestId("project-blueprint-delete-failure");
    expect(failure.textContent).toBe("Not deleted — Permission denied.");

    await waitFor(() => {
      expect(screen.getAllByTestId("project-blueprint-row")).toHaveLength(1);
    });
    expect(screen.getByTestId("project-blueprints-toolbar-count").textContent).toBe(
      "1 selected",
    );
  });

  it("holds the batch as pending and counts it down while deletes run", async () => {
    const rows = threeRows();
    listBlueprints.mockResolvedValueOnce(rows).mockResolvedValueOnce([]);
    const inFlight: { resolve: () => void }[] = [];
    deleteBlueprint.mockImplementation(
      () => new Promise<void>((resolve) => inFlight.push({ resolve })),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    fireEvent.click(screen.getByTestId("project-blueprints-select-all"));
    fireEvent.click(screen.getByTestId("project-blueprints-bulk-delete"));

    await waitFor(() => expect(inFlight).toHaveLength(1));
    expect(
      screen.getByTestId("project-blueprints-delete-progress").textContent,
    ).toBe("1 of 3");
    expect(
      screen
        .getAllByTestId("project-blueprint-row")
        .every((el) => el.getAttribute("aria-busy") === "true"),
    ).toBe(true);
    expect(
      screen.getByTestId("project-blueprints-bulk-delete").hasAttribute("disabled"),
    ).toBe(true);

    inFlight[0]!.resolve();
    await waitFor(() => expect(inFlight).toHaveLength(2));
    await waitFor(() => {
      expect(
        screen.getByTestId("project-blueprints-delete-progress").textContent,
      ).toBe("2 of 3");
    });

    inFlight[1]!.resolve();
    await waitFor(() => expect(inFlight).toHaveLength(3));
    inFlight[2]!.resolve();
    await waitFor(() => {
      expect(screen.getByTestId("project-blueprints-empty")).toBeTruthy();
    });
    expect(screen.queryByTestId("project-blueprints-error")).toBeNull();
  });

  it("treats a blueprint the host already dropped as deleted", async () => {
    listBlueprints
      .mockResolvedValueOnce(fixtureRows())
      .mockResolvedValueOnce([]);
    deleteBlueprint.mockRejectedValue(
      new LycaonApiError("blueprint not found", 404, "blueprint_not_found"),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    fireEvent.click(screen.getByTestId("project-blueprints-select-all"));
    fireEvent.click(screen.getByTestId("project-blueprints-bulk-delete"));

    await waitFor(() => {
      expect(screen.getByTestId("project-blueprints-empty")).toBeTruthy();
    });
    expect(screen.queryByTestId("project-blueprints-error")).toBeNull();
    expect(screen.queryByTestId("project-blueprint-delete-failure")).toBeNull();
  });

  it("marks the row when a single delete is refused", async () => {
    listBlueprints.mockResolvedValue(fixtureRows());
    deleteBlueprint.mockRejectedValue(
      new LycaonApiError("Disk is full.", 500, "internal_error"),
    );
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView
        projectId="proj-1"
        appStore={appStore}
        hasRepo
        onCreateSupportingSession={createSupportingSession}
      />
    ));
    await screen.findByTestId("project-blueprints-list");
    const shipRow = screen
      .getAllByTestId("project-blueprint-row")
      .find(
        (el) =>
          el.getAttribute("data-blueprint-path") ===
          ".paintedwolf/blueprints/plan.md",
      )!;
    fireEvent.click(shipRow.querySelector('[data-testid="project-blueprint-menu"]')!);
    fireEvent.click(screen.getByTestId("project-blueprint-delete"));

    const banner = await screen.findByTestId("project-blueprints-error");
    expect(banner.textContent).toBe(
      "Could not delete “Ship auth” — Disk is full.",
    );
    await waitFor(() => {
      expect(
        shipRow.querySelector('[data-testid="project-blueprint-delete-failure"]')
          ?.textContent,
      ).toBe("Not deleted — Disk is full.");
    });
    expect(screen.getAllByTestId("project-blueprint-row")).toHaveLength(2);
  });

  it("sorts by title from the list header", async () => {
    listBlueprints.mockResolvedValue(fixtureRows());
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectBlueprintsView projectId="proj-1" appStore={appStore} hasRepo onCreateSupportingSession={createSupportingSession} />
    ));
    await screen.findByTestId("project-blueprints-list");
    let titles = screen
      .getAllByTestId("project-blueprint-row")
      .map((el) => el.querySelector(".den-blueprints-row__title")?.textContent);
    expect(titles).toEqual(["Threat model", "Ship auth"]);

    fireEvent.click(screen.getByTestId("project-blueprints-sort-title"));
    titles = screen
      .getAllByTestId("project-blueprint-row")
      .map((el) => el.querySelector(".den-blueprints-row__title")?.textContent);
    expect(titles).toEqual(["Ship auth", "Threat model"]);
  });
});
