import "../../test/document-outbox-fixture.ts";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { PROJECT, ROOTS, BLUEPRINT_PATH, resetProjectFilesViewTest, sourceWorkspace } from "./project-files-view-test-harness.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { ProjectRoot } from "../../api/types.ts";
import { ProjectFilesView } from "./ProjectFilesView.tsx";
import { createAppStore } from "../../store/app-state.ts";
import { resetProjectFilesForTests } from "../documents/project-files-buffers.ts";
import { projectFilesState } from "../documents/files-buffer-state.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { fileBufferKey } from "./project-files-model.ts";

describe("ProjectFilesView stage chrome", () => {
  beforeEach(resetProjectFilesViewTest);
  afterEach(() => vi.useRealTimers());

  it("hides the tree without leaving a second navigation rail", () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));

    const toggle = screen.getByTestId("files-tree-toggle");
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(toggle);

    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    expect(
      screen
        .getByTestId("project-files-view")
        .classList.contains("project-files-view--tree-collapsed"),
    ).toBe(true);
    expect(screen.getByTestId("files-tree-pane").hidden).toBe(true);
    expect(screen.getByTestId("files-tree-resize").hidden).toBe(true);
    expect(
      screen
        .getByTestId("files-tree-scroll")
        .hasAttribute("data-overlayscrollbars"),
    ).toBe(false);
    expect(screen.getAllByTestId("files-tree-toggle")).toHaveLength(1);
    expect(screen.getByTestId("files-tab-strip")).toBeTruthy();
  });

  it("claims the navigator when showing a person-hidden tree", () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));

    fireEvent.click(screen.getByTestId("files-tree-toggle"));
    expect(
      screen
        .getByTestId("project-files-view")
        .classList.contains("project-files-view--tree-collapsed"),
    ).toBe(true);

    fireEvent.click(screen.getByTestId("files-tree-toggle"));
    const view = screen.getByTestId("project-files-view");
    expect(view.classList.contains("project-files-view--tree-collapsed")).toBe(
      false,
    );
    expect(view.classList.contains("project-files-view--tree-claimed")).toBe(
      true,
    );
    expect(screen.getByTestId("files-tree-pane").hidden).toBe(false);
  });

  it("hosts one document with the Files buffer and no stage navigation", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const onSessionChange = vi.fn();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
        document={{
          rootId: "r1",
          path: BLUEPRINT_PATH,
          initialMarkdownView: "preview",
          onSessionChange,
        }}
      />
    ));

    const host = screen.getByTestId("project-files-view");
    expect(host.classList.contains("project-files-view--document")).toBe(true);
    expect(projectFilesState(PROJECT).activeKey).toBe(
      fileBufferKey("r1", BLUEPRINT_PATH),
    );
    expect(onSessionChange).toHaveBeenCalledWith(
      expect.objectContaining({ save: expect.any(Function) }),
    );
    await waitFor(() => {
      expect(screen.getByTestId("files-editor-md-preview")).toBeTruthy();
    });
  });

  it("selects the first project root and summarizes it when no tabs are open", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));

    expect(await screen.findByTestId("files-root-summary")).toBeTruthy();
    expect(screen.getByTestId("files-root-summary-path").textContent).toBe(
      "/repo",
    );
    expect(screen.getByTestId("files-root-summary-role").textContent).toBe(
      "Primary root",
    );
    expect(screen.getByTestId("files-tab-strip")).toBeTruthy();
    expect(screen.getByTestId("files-tabs").childElementCount).toBe(0);
    const emptyToolbar = screen.getByTestId("files-editor-toolbar-empty");
    expect(
      [...emptyToolbar.querySelectorAll("button")].every(
        (button) => button.disabled,
      ),
    ).toBe(true);
    expect(
      screen.getByTestId("files-root-summary").getAttribute("data-first-time-tip-anchor"),
    ).toBe("files-project-roots");
    await waitFor(() => expect(
      document
        .querySelector('[data-testid="files-tree-dir"][data-root="r1"]')
        ?.classList.contains("den-files-tree__row--active"),
    ).toBe(true));
  });

  it("uses the chat-resolved checkout path for Files root metadata", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    appStore.actions.setCurrentSession({
      id: "session-worktree",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: PROJECT,
      workspace_path: "/repo",
      title: "Checkout",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    const resolvedRoots = ROOTS.map((root) => ({
      ...root,
      path: "/checkouts/session-worktree",
    }));
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(async () =>
        sourceWorkspace("workspace-worktree", resolvedRoots),
      ),
      // Listings answer for the workspace the lookup named.
      browseProjectSource: vi.fn(
        async (_projectId: string, input: { rootId: string; dir: string }) => ({
          workspace_id: "workspace-worktree",
          root_id: input.rootId,
          dir: input.dir,
          watch_complete: true,
          entries: [],
        }),
      ),
    });

    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    await waitFor(() => {
      expect(screen.getByTestId("files-root-summary-path").textContent).toBe(
        "/checkouts/session-worktree",
      );
    });
  });

  it("shows the selected root's summary when another root is clicked", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const roots: ProjectRoot[] = [
      ...ROOTS,
      {
        id: "r2",
        path: "/packages/docs",
        label: "docs",
        is_primary: false,
        added_at: "2026-02-01T00:00:00Z",
        kind: "attached",
      },
    ];
    const base = getLycaonClient()!;
    const client = stubFilesClient({
      ...base,
      getSourceWorkspace: vi.fn(async () => sourceWorkspace("workspace-1", roots)),
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={roots}
        client={client}
      />
    ));

    await waitFor(
      () =>
        expect(
          screen
            .getByTestId("project-files-stage-boundary")
            .getAttribute("data-ready"),
        ).toBe("true"),
      { timeout: 5000 },
    );
    fireEvent.click(screen.getByRole("button", { name: "@docs" }));

    expect(screen.getByTestId("files-root-summary-path").textContent).toBe(
      "/packages/docs",
    );
    expect(screen.getByTestId("files-root-summary-role").textContent).toBe(
      "Additional root",
    );
  });

  it("shows a compact summary when a folder is selected with no tabs open", async () => {
    resetProjectFilesForTests();
    const appStore = createAppStore();
    appStore.actions.setSidecarStatus("connected");
    const browseProjectSource = vi.fn(
      async (_projectId: string, input: { rootId: string; dir: string }) => ({
        workspace_id: "workspace-1",
        root_id: input.rootId,
        dir: input.dir,
        watch_complete: true,
        entries:
          input.dir === "."
            ? [{ name: "docs", is_dir: true }]
            : [
                { name: "guides", is_dir: true },
                { name: "README.md", is_dir: false },
              ],
      }),
    );
    const client = stubFilesClient({
      getSourceWorkspace: vi.fn(async () => ({
        workspace_id: "workspace-1",
        roots: ROOTS.map((root) => ({ id: root.id, path: root.path })),
      })),
      listProjectSourcePins: vi.fn(async () => ({ pins: [] })),
      browseProjectSource,
    });
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={client}
      />
    ));

    await waitFor(
      () =>
        expect(
          screen
            .getByTestId("project-files-stage-boundary")
            .getAttribute("data-ready"),
        ).toBe("true"),
      { timeout: 5000 },
    );
    fireEvent.click(await screen.findByRole("button", { name: "docs" }));

    expect(screen.getByTestId("files-folder-summary")).toBeTruthy();
    expect(screen.queryByTestId("files-root-summary")).toBeNull();
    expect(screen.getByTestId("files-folder-summary-path").textContent).toBe(
      "@repo/docs",
    );
    await waitFor(() =>
      expect(screen.getByTestId("files-folder-summary-count").textContent).toBe(
        "2 items",
      ),
    );
  });

  it("keeps Files controls in the sidebar without shared browse chrome", () => {
    const appStore = createAppStore();
    render(() => (
      <ProjectFilesView
        projectId={PROJECT}
        appStore={appStore}
        roots={ROOTS}
        client={null}
      />
    ));
    expect(document.querySelector(".den-browse-chrome__title")).toBeNull();
    expect(document.querySelector(".den-browse-chrome__chips")).toBeNull();
    expect(document.querySelector(".den-browse-chrome")).toBeNull();
    expect(screen.getByTestId("project-files-stage")).toBeTruthy();
  });
});
