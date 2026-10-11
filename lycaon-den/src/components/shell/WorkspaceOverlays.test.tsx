import { render } from "@solidjs/testing-library";
import { createEffect, type ComponentProps } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { WorkspaceOverlays } from "./WorkspaceOverlays.tsx";
import type { ProjectLauncher } from "../project/ProjectLauncher.tsx";
import type { Crossbar } from "../search/Crossbar.tsx";
import type { RecentChatSwitcher } from "./RecentChatSwitcher.tsx";
import type { PeerViewSwitcher } from "./PeerViewSwitcher.tsx";
import type { ShortcutHelpOverlay } from "../shortcuts/ShortcutHelpOverlay.tsx";
import type { DiffViewerDrawer } from "../source/diff/DiffViewerDrawer.tsx";
const seams = vi.hoisted(() => ({
  launcher: null as unknown as ComponentProps<typeof ProjectLauncher>, crossbar: null as unknown as ComponentProps<typeof Crossbar>,
  recent: null as unknown as ComponentProps<typeof RecentChatSwitcher>, peers: null as unknown as ComponentProps<typeof PeerViewSwitcher>,
  help: null as unknown as ComponentProps<typeof ShortcutHelpOverlay>, diff: null as unknown as ComponentProps<typeof DiffViewerDrawer>,
  focus: vi.fn(async () => undefined), close: vi.fn(async () => undefined),
}));
vi.mock("../project/ProjectLauncher.tsx", () => ({ ProjectLauncher: (p: typeof seams.launcher) => { createEffect(() => { seams.launcher = { ...p }; }); return null; } }));
vi.mock("../search/Crossbar.tsx", () => ({ Crossbar: (p: typeof seams.crossbar) => { createEffect(() => { seams.crossbar = { ...p }; }); return null; } }));
vi.mock("./RecentChatSwitcher.tsx", () => ({ RecentChatSwitcher: (p: typeof seams.recent) => { createEffect(() => { seams.recent = { ...p }; }); return null; } }));
vi.mock("./PeerViewSwitcher.tsx", () => ({ PeerViewSwitcher: (p: typeof seams.peers) => { createEffect(() => { seams.peers = { ...p }; }); return null; } }));
vi.mock("../shortcuts/ShortcutHelpOverlay.tsx", () => ({ ShortcutHelpOverlay: (p: typeof seams.help) => { createEffect(() => { seams.help = { ...p }; }); return null; } }));
vi.mock("../source/diff/DiffViewerDrawer.tsx", () => ({ DiffViewerDrawer: (p: typeof seams.diff) => { createEffect(() => { seams.diff = { ...p }; }); return null; } }));
vi.mock("../first-time-tips/FirstTimeTipsLayer.tsx", () => ({ FirstTimeTipsLayer: () => null }));
vi.mock("../../find/EditorCommandBar.tsx", () => ({ EditorCommandBar: () => <div>Find</div> }));
vi.mock("../../find/find-controller.ts", () => ({ editorCommandBarPlacement: () => "shell" }));
vi.mock("../../platform/navigation/in-app-diff.ts", () => ({ diffViewerRequest: () => ({ projectId: "project", path: "notes.txt" }) }));
vi.mock("../../platform/navigation/open-source.ts", () => ({ resolveSourceRequest: () => ({ status: "resolved", request: { absolutePath: "/garden/notes.txt", rootId: "root" } }) }));
vi.mock("../../platform/windows/item-windows.ts", () => ({ closeItemWindow: seams.close }));
vi.mock("../../platform/windows/editor-windows.ts", () => ({ focusEditorWindow: seams.focus }));

describe("workspace overlays", () => {
  it("routes launcher, search, and switcher actions using their host scope", async () => {
    const calls = { openProject: vi.fn(), newProject: vi.fn(), goHome: vi.fn(), setHomeSection: vi.fn(),
      setLauncherOpen: vi.fn(), setHelpOpen: vi.fn(), navigateFromCrossbarGoto: vi.fn(), navigateFromSearch: vi.fn(async () => undefined),
      proposeFolderAsProject: vi.fn(async () => undefined), openKeyboardSettings: vi.fn() };
    const closeCrossbar = vi.fn(); const escalateCrossbarToSearch = vi.fn();
    render(() => <WorkspaceOverlays shell={{ state: { activeProjectId: "project" } } as never}
      appStore={{ state: { currentSession: { id: "session", project_id: "project" } } } as never}
      projects={{ summaries: () => [], byId: () => ({ roots: [{ id: "root", path: "/garden", is_primary: true }] }) } as never}
      launcherOpen={() => true} helpOpen={() => true} search={{ crossbarOpen: () => true,
        crossbarArgs: () => ({ originProjectId: "project", seed: "needle", serial: 2 }), closeCrossbar, escalateCrossbarToSearch } as never}
      crossbarGotoTargets={() => []} crossbarActions={() => []} recentChatEntries={() => []} focusedPeerViews={() => []}
      recentSwitcherRef={vi.fn()} peerViewSwitcherRef={vi.fn()} navSlotWidthCss={() => "240px"} {...calls} />);
    expect(seams.crossbar.originSessionId).toBe("session"); expect(seams.crossbar.projectRoots).toHaveLength(1);
    expect(document.querySelector('[data-testid="find-bar-host"]')?.getAttribute("style")).toContain("240px");
    seams.launcher.onSwitch("other"); seams.launcher.onManageAll(); seams.launcher.onNewProject(); seams.launcher.onClose();
    expect(calls.openProject).toHaveBeenCalledWith("other"); expect(calls.setHomeSection).toHaveBeenCalledWith("all");
    expect(calls.goHome).toHaveBeenCalledOnce(); expect(calls.setLauncherOpen).toHaveBeenCalledWith(false);
    seams.crossbar.onOpenFolderAsProject?.("/other"); seams.crossbar.onClose(); seams.crossbar.onEscalate?.({ originProjectId: "project", query: "needle", mode: "evidence" });
    expect(calls.proposeFolderAsProject).toHaveBeenCalledWith("/other"); expect(closeCrossbar).toHaveBeenCalledOnce();
    seams.recent.onCommit({ projectId: "other", sessionId: "other-session" } as never);
    expect(calls.navigateFromSearch).toHaveBeenCalledWith({ projectId: "other", sessionId: "other-session" });
    const peer = { nativeLabel: "peer" } as never;
    seams.peers.onFocus(peer); seams.peers.onClose(peer); seams.peers.onClose({} as never);
    expect(seams.focus).toHaveBeenCalledWith(peer); expect(seams.close).toHaveBeenCalledExactlyOnceWith("peer");
    seams.help.onClose(); expect(calls.setHelpOpen).toHaveBeenCalledWith(false);
    expect(seams.diff.request).toMatchObject({ projectId: "project", absolutePath: "/garden/notes.txt", rootId: "root" });
    expect(seams.diff.rootRefs).toEqual([{ id: "root", path: "/garden", is_primary: true }]);
    await Promise.resolve();
  });
});
