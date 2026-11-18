import { render } from "@solidjs/testing-library";
import { createEffect, createSignal, type ComponentProps } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { WorkspaceOverlays } from "./WorkspaceOverlays.tsx";
import { createShellSearchState } from "./shell-search-state.ts";
import { crossbarServerQuery } from "../../search/crossbar-model.ts";
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

/** Overlays over the shell's own open/closed state: launcher, help, and the Crossbar's search state. */
function mountOverlays() {
  const [launcherOpen, setLauncherOpen] = createSignal(true);
  const [helpOpen, setHelpOpen] = createSignal(true);
  const [nav, setNav] = createSignal<"projects" | "search">("projects");
  const search = createShellSearchState({
    nav, showProjects: () => setNav("projects"), showSearch: () => setNav("search"),
    closeLauncher: () => setLauncherOpen(false), routeToSearch: (open: () => void) => open(), searchIsSplitCompanion: () => false,
  } as never);
  const routes = { openProject: vi.fn(), newProject: vi.fn(), goHome: vi.fn(), setHomeSection: vi.fn(),
    navigateFromCrossbarGoto: vi.fn(), navigateFromSearch: vi.fn(async () => undefined),
    proposeFolderAsProject: vi.fn(async () => undefined), openKeyboardSettings: vi.fn() };
  render(() => <WorkspaceOverlays shell={{ state: { activeProjectId: "project" } } as never}
    appStore={{ state: { currentSession: { id: "session", project_id: "project" } } } as never}
    projects={{ summaries: () => [], byId: () => ({ roots: [{ id: "root", path: "/garden", is_primary: true }] }) } as never}
    launcherOpen={launcherOpen} setLauncherOpen={setLauncherOpen} helpOpen={helpOpen} setHelpOpen={setHelpOpen} search={search}
    crossbarGotoTargets={() => []} crossbarActions={() => ({ phase: "ready", commands: [] })} recentChatEntries={() => []} focusedPeerViews={() => []}
    recentSwitcherRef={vi.fn()} peerViewSwitcherRef={vi.fn()} navSlotWidthCss={() => "240px"} {...routes} />);
  return { launcherOpen, helpOpen, nav, search, routes };
}

describe("workspace overlays", () => {
  it("opens one overlay at a time and closes each into the state that opened it", () => {
    const { launcherOpen, helpOpen, search } = mountOverlays();
    expect(seams.launcher.open).toBe(true);
    expect(seams.crossbar.open).toBe(false);

    // Opening the Crossbar takes the launcher's place.
    search.openCrossbar({ originProjectId: "project", seed: "needle" });
    expect(launcherOpen()).toBe(false);
    expect(seams.launcher.open).toBe(false);
    expect(seams.crossbar.open).toBe(true);
    expect(seams.crossbar.seed).toBe("needle");
    expect(seams.crossbar.originSessionId).toBe("session");
    expect(seams.crossbar.projectRoots).toEqual([{ id: "root", path: "/garden", is_primary: true }]);

    seams.crossbar.onClose();
    expect(search.crossbarOpen()).toBe(false);
    expect(seams.crossbar.open).toBe(false);

    seams.help.onClose();
    expect(helpOpen()).toBe(false);
    expect(seams.help.open).toBe(false);
  });

  it("escalates a Crossbar query into full search with the same origin", () => {
    const { nav, search } = mountOverlays();
    search.openCrossbar({ originProjectId: "project", seed: "needle" });
    seams.crossbar.onEscalate?.({ originProjectId: "project", query: "needle", mode: "evidence" });
    expect(nav()).toBe("search");
    expect(search.searchArgs()).toMatchObject({ originProjectId: "project", seed: crossbarServerQuery("needle", "evidence") });
  });

  it("routes launcher, search, and switcher choices to the shell", async () => {
    const { routes } = mountOverlays();
    seams.launcher.onSwitch("other"); seams.launcher.onManageAll(); seams.launcher.onNewProject();
    expect(routes.openProject).toHaveBeenCalledWith("other"); expect(routes.setHomeSection).toHaveBeenCalledWith("all");
    expect(routes.goHome).toHaveBeenCalledOnce(); expect(routes.newProject).toHaveBeenCalledOnce();
    seams.crossbar.onOpenFolderAsProject?.("/other");
    expect(routes.proposeFolderAsProject).toHaveBeenCalledWith("/other");
    seams.recent.onCommit({ projectId: "other", sessionId: "other-session" } as never);
    expect(routes.navigateFromSearch).toHaveBeenCalledWith({ projectId: "other", sessionId: "other-session" });
    const peer = { nativeLabel: "peer" } as never;
    seams.peers.onFocus(peer); seams.peers.onClose(peer); seams.peers.onClose({} as never);
    expect(seams.focus).toHaveBeenCalledWith(peer); expect(seams.close).toHaveBeenCalledExactlyOnceWith("peer");
    expect(document.querySelector('[data-testid="find-bar-host"]')?.getAttribute("style")).toContain("240px");
    expect(seams.diff.request).toMatchObject({ projectId: "project", absolutePath: "/garden/notes.txt", rootId: "root" });
    expect(seams.diff.rootRefs).toEqual([{ id: "root", path: "/garden", is_primary: true }]);
    await Promise.resolve();
  });
});
