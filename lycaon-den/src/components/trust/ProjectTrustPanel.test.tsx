import { resetProjectTrustForTests } from "../../settings/security/project-trust.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ProjectTrust } from "../../api/types.ts";
import {
  registerOpenSourceProjectLookup,
  registerOpenSourceSink,
  resetOpenSourceForTests,
} from "../../platform/navigation/open-source.ts";
import { ProjectTrustPanel } from "./ProjectTrustPanel.tsx";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { invalidateSourceQueries } from "../../files/source/source-invalidation.ts";
import { SOURCE_REFRESH_IDLE_MS } from "../../files/source/source-refresh.ts";

function suggestionTrust(projectEnabled = true): ProjectTrust {
  return {
    project_id: "project-1",
    unread_count: 0,
    review: { id: "review", project_id: "project-1", changes: [] },

    surfaces: [
      {
        id: "extension_suggestions",
        label: "Suggested extensions",
        group: "suggestion",
        count: 1,
        items: [
          {
            name: "acme/reviewed",
            root_id: "root-1",
            path: ".paintedwolf/extensions.yaml",
          },
        ],
        device_enabled: true,
        project_enabled: projectEnabled,
        applying: projectEnabled,
        seen: true,
      },
    ],
  };
}

function instructionsTrust(seen = true): ProjectTrust {
  return {
    project_id: "project-1",
    unread_count: seen ? 0 : 3,
    review: { id: "review", project_id: "project-1", changes: [] },

    surfaces: [
      {
        id: "agents_md",
        label: "Instructions",
        group: "steering",
        count: 3,
        items: [
          {
            name: "AGENTS.md",
            root_id: "root-1",
            path: "AGENTS.md",
            lines: 12,
          },
          {
            name: "backend/AGENTS.md",
            root_id: "root-1",
            path: "backend/AGENTS.md",
            lines: 8,
          },
          {
            name: ".paintedwolf/AGENTS.md",
            root_id: "root-1",
            path: ".paintedwolf/AGENTS.md",
            lines: 4,
          },
        ],
        device_enabled: true,
        project_enabled: true,
        applying: true,
        seen,
      },
    ],
  };
}

beforeEach(resetProjectTrustForTests);

afterEach(() => {
  resetOpenSourceForTests();
  vi.useRealTimers();
});

describe("ProjectTrustPanel", () => {
  it("retains the opening comparison when a source refresh arrives during capture", async () => {
    vi.useFakeTimers();
    let finishOpening!: (review: ProjectTrust) => void;
    const openProjectTrustReview = vi.fn(() => new Promise<ProjectTrust>(resolve => { finishOpening = resolve; }));
    const getProjectTrust = vi.fn(async () => instructionsTrust(false));
    render(() => <ProjectTrustPanel client={stubClient({ openProjectTrustReview, getProjectTrust })}
      projectId="project-1" projectRoots={[]} />);
    invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).not.toHaveBeenCalled();
    finishOpening(suggestionTrust());
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByTestId("project-trust-open-review")).toBeTruthy();
    expect(getProjectTrust).toHaveBeenCalledExactlyOnceWith("project-1");
    expect(openProjectTrustReview).toHaveBeenCalledTimes(1);
  });

  it("reloads retained views on return and reads source changes without marking them seen", async () => {
    vi.useFakeTimers();
    const openProjectTrustReview = vi.fn(async () => suggestionTrust());
    const changed = instructionsTrust(false);
    const getProjectTrust = vi.fn(async () => changed);
    const [active, setActive] = createSignal(false);
    const { unmount } = render(() => (
      <ResidentPresenceProvider presence={active() ? "active" : "idle"}>
        <ProjectTrustPanel client={stubClient({ openProjectTrustReview, getProjectTrust })}
          projectId="project-1" projectRoots={[]} />
      </ResidentPresenceProvider>
    ));
    invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(openProjectTrustReview).not.toHaveBeenCalled();
    expect(getProjectTrust).not.toHaveBeenCalled();

    setActive(true);
    await Promise.resolve();
    expect(screen.getByTestId("project-trust-suggestions")).toBeTruthy();
    expect(openProjectTrustReview).toHaveBeenCalledExactlyOnceWith("project-1");
    invalidateSourceQueries({ projectId: "other-project" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).not.toHaveBeenCalled();
    for (let i = 0; i < 5; i++) invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).toHaveBeenCalledExactlyOnceWith("project-1");
    expect(screen.getByTestId("project-trust-steering-row-agents_md")).toBeTruthy();
    expect(openProjectTrustReview).toHaveBeenCalledTimes(1);

    setActive(false);
    invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).toHaveBeenCalledTimes(1);
    setActive(true);
    await Promise.resolve();
    expect(openProjectTrustReview).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("project-trust-suggestions")).toBeTruthy();
    invalidateSourceQueries({ projectId: "project-1" });
    unmount();
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).toHaveBeenCalledTimes(1);
  });

  it("defers background reads until a pending trust save completes", async () => {
    vi.useFakeTimers();
    let finishSave: ((trust: ProjectTrust) => void) | undefined;
    const updateProjectTrust = vi.fn((_id: string, request: { enabled?: unknown }) =>
      request.enabled
        ? new Promise<ProjectTrust>((resolve) => { finishSave = resolve; })
        : Promise.resolve(suggestionTrust()));
    const getProjectTrust = vi.fn(async () => suggestionTrust(false));
    render(() => <ProjectTrustPanel client={stubClient({ updateProjectTrust, getProjectTrust, openProjectTrustReview: vi.fn(async () => suggestionTrust()) })}
      projectId="project-1" projectRoots={[]} />);
    await Promise.resolve();
    const toggle = screen.getByTestId("project-trust-suggestions-toggle-extension_suggestions");
    fireEvent.click(toggle);
    expect(toggle.hasAttribute("disabled")).toBe(true);
    invalidateSourceQueries({ projectId: "project-1" });
    await vi.advanceTimersByTimeAsync(SOURCE_REFRESH_IDLE_MS);
    expect(getProjectTrust).not.toHaveBeenCalled();
    finishSave?.(suggestionTrust(false));
    await vi.advanceTimersByTimeAsync(0);
    expect(getProjectTrust).toHaveBeenCalledExactlyOnceWith("project-1");
    expect(screen.getByTestId("project-trust-suggestions-toggle-extension_suggestions").hasAttribute("disabled")).toBe(false);
    expect(updateProjectTrust).toHaveBeenCalledTimes(1);
  });

  it("shows and switches extension suggestions when they are the only content", async () => {
    const updateProjectTrust = vi.fn(async (_projectId: string, request: unknown) => {
      const enabled = (request as { enabled?: Record<string, boolean> }).enabled;
      return suggestionTrust(enabled?.extension_suggestions ?? true);
    });
    const client = stubClient({
      updateProjectTrust,
      openProjectTrustReview: vi.fn(async () => suggestionTrust()),
    });

    render(() => (
      <ProjectTrustPanel client={client} projectId="project-1" projectRoots={[]} />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("project-trust-suggestions")).toBeTruthy();
    });
    expect(screen.queryByTestId("project-trust-empty")).toBeNull();

    const toggle = screen.getByTestId("project-trust-suggestions-toggle-extension_suggestions");
    fireEvent.click(toggle);
    await waitFor(() => {
      expect(updateProjectTrust).toHaveBeenCalledWith("project-1", {
        enabled: { extension_suggestions: false },
      });
    });
  });

  it("opens every AGENTS.md from Instructions detail in Files", async () => {
    const client = stubClient({
      openProjectTrustReview: vi.fn(async () => instructionsTrust()),
    });
    const opened = vi.fn();
    registerOpenSourceProjectLookup(() => ({
      roots: [{ id: "root-1", path: "/workspace/project", is_primary: true }],
    }));
    registerOpenSourceSink(opened);

    render(() => (
      <ProjectTrustPanel
        client={client}
        projectId="project-1"
        projectRoots={[{ id: "root-1", label: "project" }]}
      />
    ));

    const row = await screen.findByTestId("project-trust-steering-row-agents_md");
    expect(row.tagName).toBe("BUTTON");
    fireEvent.click(row);

    expect(await screen.findByText("AGENTS.md files")).toBeTruthy();
    expect(screen.getAllByText("AGENTS.md")).toHaveLength(3);
    expect(document.querySelector('[data-trust-folder="backend"]')).toBeTruthy();
    expect(document.querySelector('[data-trust-folder=".paintedwolf"]')).toBeTruthy();

    fireEvent.click(screen.getByTestId("project-trust-detail-item-agents_md-1"));
    await waitFor(() => {
      expect(opened).toHaveBeenCalledWith(
        expect.objectContaining({
          projectId: "project-1",
          rootId: "root-1",
          path: "backend/AGENTS.md",
          intent: "permanent",
        }),
      );
    });
  });

  it("drops a save that lands after the project changed", async () => {
    const projectATrust: ProjectTrust = {
      ...instructionsTrust(),
      project_id: "project-a",
    };
    const projectBTrust: ProjectTrust = {
      project_id: "project-b",
      unread_count: 0,
    review: { id: "review", project_id: "project-1", changes: [] },

      surfaces: [
        {
          id: "extension_suggestions",
          label: "Suggested extensions",
          group: "suggestion",
          count: 1,
          items: [
            { name: "b/only", root_id: "root-b", path: ".paintedwolf/extensions.yaml" },
          ],
          device_enabled: true,
          project_enabled: true,
          applying: true,
          seen: true,
        },
      ],
    };

    let releaseSave: (() => void) | undefined;
    const updateProjectTrust = vi.fn(
      async (projectId: string, request: { enabled?: Record<string, boolean> }) => {
        if (!request.enabled) {
          return projectId === "project-a" ? projectATrust : projectBTrust;
        }
        await new Promise<void>((resolve) => {
          releaseSave = resolve;
        });
        return projectATrust;
      },
    );
    const client = stubClient({ updateProjectTrust, openProjectTrustReview: vi.fn(async (id: string) => id === "project-a" ? projectATrust : projectBTrust) });
    const [projectId, setProjectId] = createSignal("project-a");

    render(() => (
      <ProjectTrustPanel
        client={client}
        projectId={projectId()}
        projectRoots={[]}
      />
    ));

    await screen.findByTestId("project-trust-steering-row-agents_md");
    fireEvent.click(
      screen.getByTestId("project-trust-steering-toggle-agents_md"),
    );
    await waitFor(() => expect(releaseSave).toBeDefined());

    setProjectId("project-b");
    await screen.findByTestId("project-trust-suggestions");

    releaseSave?.();
    await updateProjectTrust.mock.results[0]?.value;
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(screen.queryByTestId("project-trust-steering-row-agents_md")).toBeNull();
    expect(screen.getByTestId("project-trust-suggestions")).toBeTruthy();
  });
});
