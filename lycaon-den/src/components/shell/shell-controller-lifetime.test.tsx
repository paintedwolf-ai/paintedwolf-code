import { render, waitFor } from "@solidjs/testing-library";
import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createShellAttention } from "./shell-attention.ts";
import { createStageAvailability } from "./shell-stage-placement.ts";
import { createShellFocusRequest } from "./shell-commands.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { registerFocusRegion, resetFocusRegionsForTests } from "../../shortcuts/focus-region.ts";

const seams = vi.hoisted(() => ({ seen: vi.fn(), scanners: vi.fn(), focused: true }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => ({ markSessionSeen: seams.seen, getSecurityScannersSettings: seams.scanners }) }));
vi.mock("../../platform/windows/window-chrome.ts", () => ({ windowFocused: () => seams.focused, widenWindowBy: vi.fn(async () => "claimed") }));

afterEach(() => { vi.clearAllMocks(); resetFocusRegionsForTests(); });
describe("shell controller ownership", () => {
  it("marks only a readable conversation and updates MRU from host session state", async () => {
    const shell = createShellStore("connected");
    const appStore = createAppStore();
    const session = { id: "session", project_id: "project", status: "idle", seen_at: "2026-10-11T10:00:00Z" } as never;
    appStore.actions.setCurrentSession(session);
    shell.commitStageScope({ projectId: "project", sessionId: "session" });
    const [shown, setShown] = createSignal(false);
    seams.seen.mockResolvedValue(session);
    let controller!: ReturnType<typeof createShellAttention>;
    const view = render(() => {
      controller = createShellAttention({ shell, appStore, projects: { state: { projects: [{ id: "project", name: "  Garden  " }] } } as never,
        recents: { state: { recents: [{ projectId: "project", sessionId: "session", title: "Garden chat", lastActivityAt: 3 }] } } as never,
        attention: { index: () => new Map() } as never, presentedChat: () => ({ projectId: "project", sessionId: "session" }),
        workspaceMounted: () => true, conversationShown: shown });
      return null;
    });
    expect(seams.seen).not.toHaveBeenCalled();
    expect(controller.recentChatEntries()).toEqual(expect.arrayContaining([expect.objectContaining({ projectId: "project", sessionId: "session" })]));
    setShown(true);
    await waitFor(() => expect(seams.seen).toHaveBeenCalledWith("session"));
    expect(appStore.state.currentSession?.id).toBe("session");
    controller.attentionIndex();
    view.unmount(); seams.seen.mockClear();
    setShown(false); setShown(true);
    expect(seams.seen).not.toHaveBeenCalled();
  });

  it("uses the host feature response and routes away from disabled security", async () => {
    const appStore = createAppStore(); const shell = createShellStore("connected");
    const [nav, setNav] = createSignal<"security" | "projects">("security");
    seams.scanners.mockResolvedValue({ enabled: false });
    let controller!: ReturnType<typeof createStageAvailability>;
    render(() => {
      controller = createStageAvailability({ appStore, shell, nav, setNav: (value) => setNav(value as "security" | "projects") });
      return null;
    });
    await waitFor(() => expect(controller.stageAvailable("security")).toBe(false));
    expect(controller.stageFeatureFlags()).toEqual({ securityScannersEnabled: false });
    controller.setSecurityScannersFeature(false);
    expect(nav()).toBe("projects");
    controller.setSecurityScannersFeature(true); expect(controller.stageAvailable("security")).toBe(true);
  });

  it("waits for a mounted focus region and clears its pending focus request", async () => {
    let setPending!: ReturnType<typeof createShellFocusRequest>;
    let dispose!: () => void;
    createRoot((d) => { dispose = d; setPending = createShellFocusRequest(); });
    const element = document.createElement("textarea"); document.body.append(element);
    setPending("composer"); expect(document.activeElement).not.toBe(element);
    registerFocusRegion("composer", element, {});
    await waitFor(() => expect(document.activeElement).toBe(element));
    element.blur();
    registerFocusRegion("composer", element, {});
    await Promise.resolve(); await Promise.resolve();
    expect(document.activeElement).not.toBe(element);
    dispose(); element.remove();
  });
});
