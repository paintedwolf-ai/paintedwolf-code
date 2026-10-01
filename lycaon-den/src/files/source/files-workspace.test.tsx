import { createEffect, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import type { SourceBrowseTarget } from "../../api/http-capabilities/source.ts";
import type { ProjectRoot } from "../../api/types.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { createFilesWorkspace } from "./files-workspace.ts";
import { bindWorktree, unbindWorktree } from "../../chat/actions/git-actions.ts";
import { createAppStore } from "../../store/app-state.ts";
import { invalidateWorkspace } from "./workspace-invalidation.ts";
import { PROJECT, ROOTS, resetProjectFilesViewTest, sourceWorkspace } from "../components/project-files-view-test-harness.ts";
import { applySourceTreeChanges } from "../tree/source-tree-store.ts";

describe("mounted workspace root metadata", () => {
  beforeEach(resetProjectFilesViewTest);

  it("shares live directory identities across create, rename, delete and workspace switches", async () => {
    let workspaceId = "listing-original";
    const rootId = ROOTS[0]!.id;
    const client = stubClient({
      ...getLycaonClient()!,
      getSourceWorkspace: async () => sourceWorkspace(workspaceId, ROOTS),
      browseProjectSource: async (_project: string, input: SourceBrowseTarget = {}) => ({
        workspace_id: workspaceId, root_id: input.rootId ?? rootId, dir: input.dir ?? ".",
        watch_complete: true, entries: workspaceId === "listing-original" ? [{ name: "original.txt", is_dir: false }] : [],
      }),
    });
    const mounted = render(() => {
      const workspace = createFilesWorkspace({
        projectId: PROJECT, roots: () => ROOTS, client: () => client, chatSessionId: () => undefined,
        reachable: () => true, restoreHotExit: () => false,
      });
      return <output data-testid="directory">{JSON.stringify(workspace.directoryEntries(rootId, ".")?.map((entry) => entry.name))}</output>;
    });
    await waitFor(() => expect(screen.getByTestId("directory").textContent).toBe('["original.txt"]'));
    const change = (op: "create" | "rename" | "delete", path: string, fromPath?: string) => {
      applySourceTreeChanges({ project_id: PROJECT, workspace_id: workspaceId, workspace_kind: "project", resync: false,
        changes: [{ root_id: rootId, op, path, from_path: fromPath, is_dir: false, origin: "user", changed_at: new Date().toISOString() }] });
    };
    change("create", "new ü.txt");
    expect(screen.getByTestId("directory").textContent).toContain("new ü.txt");
    change("rename", "renamed.txt", "new ü.txt");
    expect(screen.getByTestId("directory").textContent).toContain("renamed.txt");
    expect(screen.getByTestId("directory").textContent).not.toContain("new ü.txt");
    change("delete", "original.txt");
    expect(screen.getByTestId("directory").textContent).toBe('["renamed.txt"]');
    workspaceId = "listing-replacement";
    invalidateWorkspace(client, PROJECT, undefined);
    await waitFor(() => expect(screen.getByTestId("directory").textContent).toBe("[]"));
    mounted.unmount();
  });

  it("follows worktree creation and removal without navigation and isolates other scopes", async () => {
    let bound = false;
    const sessionId = "chat-worktree";
    const appStore = createAppStore();
    const worktreeView = () => ({
      bound, session_id: sessionId, dirty: false, ahead_of_base: 0, behind_base: 0,
    });
    const roots = () => ROOTS.map((root) => ({ ...root, path: bound ? "/chat-checkout" : root.path }));
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace(bound ? "chat-workspace" : "base-workspace", roots(), bound));
    const client = stubClient({
      ...getLycaonClient()!, getSourceWorkspace,
      bindGitWorktree: async () => { bound = true; return worktreeView(); },
      deleteGitWorktree: async () => { bound = false; },
      listGitRepos: async () => ({ repos: [], active_repo_id: "" }),
      browseProjectSource: async (_projectId: string, input: SourceBrowseTarget = {}) => ({
        workspace_id: bound ? "chat-workspace" : "base-workspace",
        root_id: input.rootId ?? ROOTS[0]!.id, dir: input.dir ?? "", watch_complete: true, entries: [],
      }),
    });
    const mounted = render(() => {
      const workspace = createFilesWorkspace({
        projectId: PROJECT, roots: () => ROOTS, client: () => client, chatSessionId: () => sessionId,
        reachable: () => true, restoreHotExit: () => false,
      });
      return <output data-testid="workspace">{JSON.stringify(workspace.filesRoots())}</output>;
    });
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(ROOTS)));
    const initialReads = getSourceWorkspace.mock.calls.length;
    invalidateWorkspace(client, "another-project", sessionId);
    invalidateWorkspace(client, PROJECT, "another-chat");
    invalidateWorkspace(stubClient({}), PROJECT, sessionId);
    await Promise.resolve();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(initialReads);

    await bindWorktree(appStore, client, PROJECT, sessionId, "repo", "chore/check-worktree");
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(roots())));
    expect(screen.getByTestId("workspace").textContent).toContain("/chat-checkout");
    await unbindWorktree(appStore, client, PROJECT, sessionId);
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(ROOTS)));
    client.bindGitWorktree = async () => {
      bound = true;
      throw new Error("Binding retained after partial create");
    };
    await expect(bindWorktree(appStore, client, PROJECT, sessionId, "repo")).rejects.toThrow("Binding retained");
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(roots())));
    expect(screen.getByTestId("workspace").textContent).toContain("/chat-checkout");
    mounted.unmount();
    const finalReads = getSourceWorkspace.mock.calls.length;
    invalidateWorkspace(client, PROJECT, sessionId);
    await Promise.resolve();
    expect(getSourceWorkspace).toHaveBeenCalledTimes(finalReads);
  });

  it("resolves a chat that shares the project checkout without unsettling or re-listing anything", async () => {
    const [chat, setChat] = createSignal<string | undefined>("chat-a");
    const getSourceWorkspace = vi.fn(async () => sourceWorkspace("workspace-shared", ROOTS, false));
    const browseProjectSource = vi.fn(async (_projectId: string, input: SourceBrowseTarget = {}, _sessionId?: string) => ({
      workspace_id: "workspace-shared", root_id: input.rootId ?? ROOTS[0]!.id, dir: input.dir ?? ".",
      watch_complete: true, entries: [{ name: "shared.txt", is_dir: false }],
    }));
    const client = stubClient({ ...getLycaonClient()!, getSourceWorkspace, browseProjectSource });
    const settled: boolean[] = [];
    let workspace!: ReturnType<typeof createFilesWorkspace>;
    render(() => {
      workspace = createFilesWorkspace({
        projectId: PROJECT, roots: () => ROOTS, client: () => client, chatSessionId: chat,
        reachable: () => true, restoreHotExit: () => false,
      });
      createEffect(() => { settled.push(workspace.workspaceSettled()); });
      return <output data-testid="directory">{JSON.stringify(workspace.directoryEntries(ROOTS[0]!.id, ".")?.map((entry) => entry.name))}</output>;
    });
    await waitFor(() => expect(screen.getByTestId("directory").textContent).toBe('["shared.txt"]'));
    const roots = workspace.filesRoots();
    const listings = browseProjectSource.mock.calls.length;
    expect(workspace.filesSourceAddress()).toBeUndefined();

    setChat("chat-b");
    await waitFor(() => expect(getSourceWorkspace).toHaveBeenCalledTimes(2));
    await Promise.resolve();

    expect(workspace.filesRoots()).toBe(roots);
    expect(workspace.filesSourceAddress()).toBeUndefined();
    expect(browseProjectSource).toHaveBeenCalledTimes(listings);
    // Settled once, and never withdrawn: the tree is not dimmed or held.
    expect(settled.slice(settled.indexOf(true))).not.toContain(false);
    expect(screen.getByTestId("directory").textContent).toBe('["shared.txt"]');
  });

  it("addresses a chat's own worktree by that chat and moves to another chat's worktree", async () => {
    const [chat, setChat] = createSignal<string | undefined>("chat-a");
    const getSourceWorkspace = vi.fn(async (_projectId: string, sessionId?: string) =>
      sourceWorkspace(`workspace-${sessionId}`, ROOTS.map((root) => ({ ...root, path: `/checkout-${sessionId}` })), true));
    const browseProjectSource = vi.fn(async (_projectId: string, input: SourceBrowseTarget = {}, sessionId?: string) => ({
      workspace_id: `workspace-${sessionId}`, root_id: input.rootId ?? ROOTS[0]!.id, dir: input.dir ?? ".",
      watch_complete: true, entries: [{ name: `${sessionId}.txt`, is_dir: false }],
    }));
    const client = stubClient({ ...getLycaonClient()!, getSourceWorkspace, browseProjectSource });
    let workspace!: ReturnType<typeof createFilesWorkspace>;
    render(() => {
      workspace = createFilesWorkspace({
        projectId: PROJECT, roots: () => ROOTS, client: () => client, chatSessionId: chat,
        reachable: () => true, restoreHotExit: () => false,
      });
      return <output data-testid="directory">{JSON.stringify(workspace.directoryEntries(ROOTS[0]!.id, ".")?.map((entry) => entry.name))}</output>;
    });
    await waitFor(() => expect(screen.getByTestId("directory").textContent).toBe('["chat-a.txt"]'));
    expect(workspace.filesSourceAddress()).toBe("chat-a");

    setChat("chat-b");
    await waitFor(() => expect(screen.getByTestId("directory").textContent).toBe('["chat-b.txt"]'));
    expect(workspace.filesSourceAddress()).toBe("chat-b");
    expect(workspace.filesWorkspaceId()).toBe("workspace-chat-b");
  });

  it.each([
    { label: "reference-café" },
    { path: "/another-checkout" },
    { is_primary: false },
    { kind: "draft" as const },
  ])("publishes changed root metadata %j without remounting", async (change: Partial<ProjectRoot>) => {
    const [roots, setRoots] = createSignal(ROOTS.map((root) => ({ ...root })));
    const client = stubClient({
      ...getLycaonClient()!,
      getSourceWorkspace: async () => sourceWorkspace("workspace-roots", roots()),
      browseProjectSource: async (_projectId: string, input: SourceBrowseTarget = {}) => ({
        workspace_id: "workspace-roots", root_id: input.rootId ?? ROOTS[0]!.id, dir: input.dir ?? "",
        watch_complete: true, entries: [],
      }),
    });
    render(() => {
      const workspace = createFilesWorkspace({
        projectId: PROJECT, roots, client: () => client, chatSessionId: () => undefined,
        reachable: () => true, restoreHotExit: () => false,
      });
      return <output data-testid="workspace">{workspace.workspaceSettled() ? JSON.stringify(workspace.filesRoots()) : "pending"}</output>;
    });
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(ROOTS)));
    const updated = ROOTS.map((root) => ({ ...root, ...change }));
    setRoots(updated);
    await waitFor(() => expect(screen.getByTestId("workspace").textContent).toBe(JSON.stringify(updated)));
  });
});
