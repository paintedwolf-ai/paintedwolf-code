import { render } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { installShellNavigationSinks } from "./shell-navigation-sinks.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import type { registerComposerAttachmentSink } from "../../chat/composer/add-to-chat.ts";
import type { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import type { setShellNoticeActionSinks } from "../../notices/notice-actions.ts";
const seams = vi.hoisted(() => ({
  attachment: null as unknown as Parameters<typeof registerComposerAttachmentSink>[0],
  status: null as Parameters<typeof setStatusChipNavigationSink>[0], notice: null as Parameters<typeof setShellNoticeActionSinks>[0],
  detach: vi.fn(), itemMirror: vi.fn(), workspaceMirror: vi.fn(),
}));
vi.mock("../../chat/composer/add-to-chat.ts", () => ({ registerComposerAttachmentSink: (p: typeof seams.attachment) => { seams.attachment = p; return seams.detach; } }));
vi.mock("../../chat/status/status-navigation-sink.ts", () => ({ setStatusChipNavigationSink: (p: typeof seams.status) => { seams.status = p; } }));
vi.mock("../../notices/notice-actions.ts", () => ({ setShellNoticeActionSinks: (p: typeof seams.notice) => { seams.notice = p; } }));
vi.mock("../../platform/windows/item-windows.ts", async (original) => ({ ...(await original<typeof import("../../platform/windows/item-windows.ts")>()), startItemWindowViewMirror: seams.itemMirror }));
vi.mock("../../platform/windows/workspace-view-registry.ts", async (original) => ({ ...(await original<typeof import("../../platform/windows/workspace-view-registry.ts")>()), startWorkspaceViewPresentationMirror: seams.workspaceMirror }));

describe("shell navigation sink lifetime", () => {
  it("routes status and attachment actions and disposes mirrors that resolve after unmount", async () => {
    let finishItems!: (stop: () => void) => void; let finishWorkspace!: (stop: () => void) => void;
    seams.itemMirror.mockReturnValue(new Promise<void>((resolve) => { finishItems = resolve as never; }));
    seams.workspaceMirror.mockReturnValue(new Promise<void>((resolve) => { finishWorkspace = resolve as never; }));
    const shell = createShellStore("connected"); shell.commitStageScope({ projectId: "project", sessionId: "session" });
    const appStore = createAppStore(); appStore.actions.setSidecarStatus("connected");
    const calls = { openStageWithPlacement: vi.fn(), openSearch: vi.fn(), openNotificationSession: vi.fn(), revealConversation: vi.fn(async () => true),
      resumeSession: vi.fn(async () => undefined), openWorkers: vi.fn(), closeSearch: vi.fn(), closeCrossbar: vi.fn(), setNav: vi.fn(),
      openSettings: vi.fn(), selectContextSection: vi.fn() };
    const view = render(() => {
      installShellNavigationSinks({ shell, appStore, projects: { byId: () => ({ roots: [{ id: "root" }] }) } as never,
        activeChat: () => ({ projectId: "project", sessionId: "session" }), showNoProviderBanner: () => false, ...calls });
      return null;
    });
    seams.notice?.openAIProviders?.(); expect(calls.openSettings).toHaveBeenCalledWith("providers");
    seams.status?.({ kind: "project-context", stage: "security" });
    seams.status?.({ kind: "project-configuration", section: "trust" });
    seams.status?.({ kind: "settings", section: "providers" });
    expect(calls.openStageWithPlacement).toHaveBeenCalledWith("security", "routed"); expect(calls.selectContextSection).toHaveBeenCalledWith("trust");
    expect(seams.attachment.sessionId()).toBe("session"); expect(seams.attachment.projectId()).toBe("project");
    expect(seams.attachment.projectRoots()).toEqual([{ id: "root" }]); expect(seams.attachment.blockReason()).toBeNull();
    seams.attachment.reveal(); expect(calls.setNav).toHaveBeenCalledWith("projects"); expect(calls.closeSearch).toHaveBeenCalledOnce();
    const lookup = () => ({ roots: [{ id: "root", path: "/repo", label: "repo", is_primary: true }] });
    await openSourceLocation({
      projectId: "other-project",
      rootId: "root",
      path: "src/a.ts",
      repoId: "repo-1",
      worktreeId: "wt-1",
      selectCheckout: true,
      entryKind: "file",
      intent: "permanent",
    }, { lookup });
    expect(appStore.state.gitWorktreeTargets["other-project\0repo-1"]).toBe("wt-1");
    await openSourceLocation({
      projectId: "project",
      rootId: "root",
      path: "src",
      worktreeId: "wt-1",
      entryKind: "folder",
      action: "reveal",
      intent: "permanent",
    }, { lookup });
    openFilesSurface({ kind: "stage", projectId: "project" });
    expect(calls.openStageWithPlacement).toHaveBeenCalledWith("files", "routed");
    view.unmount(); expect(seams.status).toBeNull(); expect(seams.notice).toBeNull(); expect(seams.detach).toHaveBeenCalledOnce();
    const stopItems = vi.fn(); const stopWorkspace = vi.fn(); finishItems(stopItems); finishWorkspace(stopWorkspace);
    await Promise.resolve();
    expect(stopItems).toHaveBeenCalledOnce(); expect(stopWorkspace).toHaveBeenCalledOnce();
  });
});
