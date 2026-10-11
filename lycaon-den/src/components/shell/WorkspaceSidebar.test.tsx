import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createEffect, createSignal, type ComponentProps } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkspaceSidebar } from "./WorkspaceSidebar.tsx";
import { createShellStore } from "../../store/shell-store.ts";
import type { FocusedProjectNav } from "../nav/FocusedProjectNav.tsx";
import type { LayoutDockTray } from "./LayoutDock.tsx";
const seams = vi.hoisted(() => ({
  nav: null as unknown as ComponentProps<typeof FocusedProjectNav>,
  layout: null as unknown as ComponentProps<typeof LayoutDockTray>,
  pin: vi.fn(async () => undefined), movePin: vi.fn(async () => undefined), focus: vi.fn(async () => undefined), close: vi.fn(async () => undefined),
}));
vi.mock("../nav/FocusedProjectNav.tsx", () => ({ FocusedProjectNav: (p: typeof seams.nav) => { createEffect(() => { seams.nav = { ...p }; }); return <button class="focused-session-list__row" aria-current="true">Current chat</button>; } }));
vi.mock("./LayoutDock.tsx", () => ({ LayoutDockTray: (p: typeof seams.layout) => { createEffect(() => { seams.layout = { ...p }; }); return null; }, LayoutDockButton: () => null }));
vi.mock("../chatview/ComposerStatusChips.tsx", () => ({ ComposerStatusChips: () => null }));
vi.mock("../../chat/session/session-retirement.ts", () => ({ setSessionPinned: seams.pin, moveSessionPin: seams.movePin }));
vi.mock("../../platform/windows/item-windows.ts", () => ({ focusItemWindow: seams.focus, closeItemWindow: seams.close }));

function mountSidebar() {
  const shell = createShellStore("connected"); shell.commitStageScope({ projectId: "project", sessionId: "session" });
  const [collapsed, setCollapsed] = createSignal(false);
  const [nav, setNav] = createSignal("projects");
  const [tray, setTray] = createSignal("layout");
  const [stage, setStage] = createSignal<string | null>("files");
  const calls = {
    hideWindowNav: vi.fn(), toggleConversation: vi.fn(), toggleContext: vi.fn(), goHome: vi.fn(), startNewChat: vi.fn(async () => undefined),
    selectSidebarSession: vi.fn(async () => undefined), archiveSidebarSession: vi.fn(async () => undefined), deleteSidebarSession: vi.fn(async () => undefined),
    openSessionInNewWindow: vi.fn(), renameSession: vi.fn(async () => undefined), showStage: vi.fn(), goToContextStage: vi.fn(),
    detachStageToWindow: vi.fn(async () => undefined), closeStageWindows: vi.fn(), openConfiguration: vi.fn(),
    openSettings: vi.fn(), closeSettingsNav: vi.fn(), closeLayoutTray: vi.fn(), toggleLayoutTray: vi.fn(), selectSettingsSection: vi.fn(),
    applyStagePlacement: vi.fn(async () => undefined), setHomeSection: vi.fn(), setLauncherOpen: vi.fn(),
  };
  const projectLifecycle = { activeProjectRoots: () => [], addFolderToActiveProject: vi.fn(async () => undefined), detachFolderFromActiveProject: vi.fn(async () => undefined),
    retryActiveProjectPromotion: vi.fn(async () => undefined), cancelActiveProjectPromotion: vi.fn(async () => undefined), openMoveDialog: vi.fn() };
  const peerWindows = { sessionWindowIds: () => new Set(), sessionWindowCounts: () => new Map(), sessionWindowViewNumbers: () => new Map(),
    detachedStageIds: () => new Set(), contextWindowViewNumbers: () => new Map(), projectPeerViews: () => [], stageInPeerWindow: () => false,
    raiseSessionWindow: vi.fn(), closeSessionWindow: vi.fn(), raiseStageWindow: vi.fn(), closeStageWindow: vi.fn() };
  const search = { searchOpen: () => false, crossbarOpen: () => false, closeSearch: vi.fn(), closeCrossbar: vi.fn(), openSearch: vi.fn() };
  const row = { projectId: "project", sessionId: "session", title: "Current chat" };
  const props = { shell, appStore: {}, projects: { summary: () => ({ displayName: "Project" }), byId: () => ({ roots: [] }) },
    projectSessions: { state: { sort: "activity", rowsSort: "activity", total: 1 }, refresh: vi.fn(async () => undefined) },
    activeProject: () => ({ is_draft: true, promotion: { phase: "copying" } }), activeChat: () => row, readyChat: () => row,
    nav, setNav, stageAvailable: () => true, splitLive: () => true, dockTray: tray, settingsSection: () => "general",
    windowNavCollapsed: collapsed, navSlotWidthCss: () => "240px", contextHidden: () => false,
    sessionRetirementDeps: () => ({ host: "host" }), sidebarRowAsRecent: () => row, projectLifecycle, peerWindows, search,
    settingsStore: {}, costStore: {}, notices: { index: () => new Map() }, homeSection: () => "recents", homeSummaries: () => [],
    sidebarSections: () => [], sidebarChatsPublished: () => true, attentionIndex: () => new Map(), identityReady: () => true,
    workspaceRevealed: () => true, onFreshSession: () => false, stageEntryActive: () => true,
    conversationInLayout: () => true, railStatusProject: () => "project", railStatusSessionId: () => "session",
    layoutTrayStageId: stage, stageColumnStageId: stage, stageChromeDeficitPx: () => 50, presentedSplitLive: () => true,
    layoutTriggerRef: vi.fn(), ...calls,
  } as unknown as ComponentProps<typeof WorkspaceSidebar>;
  return { ...render(() => <WorkspaceSidebar {...props} />), calls, projectLifecycle, peerWindows, search, row, setCollapsed, setNav, setTray, setStage };
}

beforeEach(() => vi.clearAllMocks());
describe("workspace sidebar composition", () => {
  it("keeps row and project actions bound to the current host inventory", async () => {
    const { calls, projectLifecycle, peerWindows, row } = mountSidebar();
    expect(seams.nav.promotePending).toBe(true); expect(seams.nav.sessionsLoaded).toBe(true);
    seams.nav.onNewChat("project"); seams.nav.onSelectSession(row as never); await seams.nav.onRenameSession(row as never, "Renamed");
    seams.nav.onTogglePin(row as never, true); seams.nav.onMovePin(row as never, 2);
    seams.nav.onArchiveSession(row as never); seams.nav.onDeleteSession(row as never);
    expect(calls.renameSession).toHaveBeenCalledWith({ projectId: "project", sessionId: "session" }, "Renamed");
    expect(seams.pin).toHaveBeenCalledWith({ host: "host" }, row, true); expect(seams.movePin).toHaveBeenCalledWith({ host: "host" }, row, 2);
    seams.nav.onRaiseSessionWindow?.(row as never, 2); seams.nav.onCloseSessionWindow?.(row as never, 2);
    expect(peerWindows.raiseSessionWindow).toHaveBeenCalledWith("session", 2); expect(peerWindows.closeSessionWindow).toHaveBeenCalledWith("session", 2);
    seams.nav.onAddFolder?.(); seams.nav.onDetachFolder?.("root"); seams.nav.onSaveDraft?.(); seams.nav.onRetryPromotion?.(); seams.nav.onCancelPromotion?.();
    expect(projectLifecycle.openMoveDialog).toHaveBeenCalledWith("project"); expect(projectLifecycle.detachFolderFromActiveProject).toHaveBeenCalledWith("root");
    seams.nav.onOpenAllChats(); seams.nav.onOpenLauncher(); seams.nav.onOpenStage("files");
    expect(calls.showStage).toHaveBeenCalledWith("chats"); expect(calls.setLauncherOpen).toHaveBeenCalledWith(true);
    expect(seams.nav.isStageAvailable("files")).toBe(true); expect(seams.nav.isStageActive("files")).toBe(true);
    seams.nav.onOpenInNewWindow?.("files"); seams.nav.onFocusProjectView?.("peer"); seams.nav.onCloseProjectView?.("peer");
    expect(calls.detachStageToWindow).toHaveBeenCalledWith("files"); expect(seams.focus).toHaveBeenCalledWith("peer"); expect(seams.close).toHaveBeenCalledWith("peer");
    await Promise.resolve();
  });

  it("routes settings and layout placement and releases sidebar focus when collapsed", async () => {
    const { calls, search, setNav, setStage, setCollapsed } = mountSidebar();
    const settings = screen.getByTestId("nav-settings");
    fireEvent.click(settings); expect(calls.openSettings).toHaveBeenCalledOnce(); expect(search.closeSearch).toHaveBeenCalledOnce();
    setNav("settings"); fireEvent.click(settings); expect(calls.closeSettingsNav).toHaveBeenCalledOnce();
    expect(seams.layout.widensFor?.("split")).toBe(true);
    seams.layout.onPlacement?.("split"); expect(calls.applyStagePlacement).toHaveBeenCalledWith("files", "split");
    seams.layout.onStageWindowChange?.(true); seams.layout.onStageWindowChange?.(false);
    expect(calls.detachStageToWindow).toHaveBeenCalledWith("files"); expect(calls.closeStageWindows).toHaveBeenCalledWith("files");
    seams.layout.onToggleConversation?.(); seams.layout.onToggleContext?.();
    expect(calls.toggleConversation).toHaveBeenCalledOnce(); expect(calls.toggleContext).toHaveBeenCalledOnce();
    setStage(null); seams.layout.onPlacement?.("inline"); expect(calls.applyStagePlacement).toHaveBeenCalledOnce();
    setCollapsed(true); expect(document.querySelector("aside")?.getAttribute("aria-hidden")).toBe("true");
    setCollapsed(false); expect(document.querySelector("aside")?.getAttribute("aria-hidden")).toBe("false");
    await Promise.resolve();
  });
});
