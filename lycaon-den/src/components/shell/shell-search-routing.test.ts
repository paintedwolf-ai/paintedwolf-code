// @vitest-environment jsdom
import { createRoot } from "solid-js";
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { createShellSearchNavigation } from "./shell-search-navigation.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { pendingSettingReveal } from "../../settings/settings-reveal.ts";

vi.mock("../../platform/connection/app-connection.ts", () => ({ noticeReporterFor: () => ({ reportError: vi.fn() }) }));
vi.mock("../../chat/transcript/presentation/transcript-reveal.ts", () => ({ revealChicklet: vi.fn(async () => true) }));

describe("shell go-to routing", () => {
  it("retains routed return behavior across settings, stages, projects, and sessions", async () => {
    const calls = { closeSearch: vi.fn(), closeLauncher: vi.fn(), showProjects: vi.fn(), openProject: vi.fn(),
      resumeSession: vi.fn(async () => undefined), openWorkers: vi.fn(), setNav: vi.fn(), selectSettingsSection: vi.fn(),
      openSettings: vi.fn(), openStageWithPlacement: vi.fn(), openConfiguration: vi.fn(), goHome: vi.fn() };
    const routeToStage = vi.fn((_stage: string, open: () => void) => open());
    const navigation = createRoot((dispose) => {
      onTestFinished(dispose);
      return createShellSearchNavigation({ shell: createShellStore("connected"), recents: { state: { recents: [] } } as never,
        projects: { state: { projects: [] } } as never, activeProject: () => undefined, attentionIndex: () => new Map(),
        securityScannersFeatureOn: () => true, routeToStage, ...calls });
    });
    navigation.navigateFromCrossbarGoto({ kind: "setting", settingId: "appearance", label: "Appearance" });
    expect(routeToStage).toHaveBeenCalledWith("settings", expect.any(Function));
    expect(calls.selectSettingsSection).toHaveBeenCalledWith("general", expect.objectContaining({ general: "display" }));
    expect(pendingSettingReveal()?.setting.id).toBe("appearance");
    navigation.navigateFromCrossbarGoto({ kind: "setting", settingId: "missing-setting", label: "Missing" });
    expect(routeToStage).toHaveBeenCalledOnce();
    navigation.navigateFromCrossbarGoto({ kind: "project", projectId: "project", label: "Project" });
    expect(calls.openProject).toHaveBeenCalledWith("project");
    for (const surface of ["files", "context", "chats", "home", "settings"] as const) {
      navigation.navigateFromCrossbarGoto({ kind: "surface", surface, label: surface });
    }
    expect(calls.openStageWithPlacement).toHaveBeenCalledWith("files", "routed");
    expect(calls.openConfiguration).toHaveBeenCalledOnce(); expect(calls.setNav).toHaveBeenCalledWith("chats");
    expect(calls.goHome).toHaveBeenCalledOnce(); expect(calls.openSettings).toHaveBeenCalledOnce();
    navigation.navigateFromCrossbarGoto({ kind: "session", projectId: "project", sessionId: "session", label: "Chat" });
    await Promise.resolve();
    expect(calls.resumeSession).toHaveBeenCalledWith({ projectId: "project", sessionId: "session", title: "" }, expect.objectContaining({ stageSwitchDone: true }));
    await navigation.navigateFromSearch({ projectId: "project" });
    expect(calls.openProject).toHaveBeenCalledTimes(2);
  });
});
