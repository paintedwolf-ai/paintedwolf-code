import { render } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { createShellContributions } from "./shell-contributions.ts";
import { createShellStore } from "../../store/shell-store.ts";
import { createAppStore } from "../../store/app-state.ts";
import type { registerUIEffectSink } from "../../contributions/dispatch.ts";
const seams = vi.hoisted(() => ({ effects: null as unknown as Parameters<typeof registerUIEffectSink>[0],
  invoke: vi.fn(), stop: vi.fn(), prefill: vi.fn(async () => undefined) }));
vi.mock("../../contributions/dispatch.ts", async (original) => ({ ...(await original<typeof import("../../contributions/dispatch.ts")>()),
  registerUIEffectSink: (sink: typeof seams.effects) => { seams.effects = sink; return seams.stop; } }));
vi.mock("../../shortcuts/dispatcher.ts", async (original) => ({ ...(await original<typeof import("../../shortcuts/dispatcher.ts")>()), invokeCommand: seams.invoke }));
vi.mock("../../chat/composer/add-to-chat.ts", () => ({ runComposerPrefillEffect: seams.prefill }));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => null }));

describe("shell contribution effects", () => {
  it("routes host-approved destinations through the shell command vocabulary and cleans up", () => {
    const shell = createShellStore("connected"); shell.commitStageScope({ projectId: "project", sessionId: "session" });
    const appStore = createAppStore(); const openStageWithPlacement = vi.fn();
    const [nav, setNav] = createSignal<"projects" | "chats" | "files" | "settings">("projects");
    const view = render(() => {
      createShellContributions({ shell, appStore, activeChat: () => ({ projectId: "project", sessionId: "session" }),
        readyChat: () => ({ projectId: "project", sessionId: "session" }), nav, windowNavCollapsed: () => false,
        contextHidden: () => false, splitLive: () => false, stageColumnStageId: () => null,
        stageFeatureFlags: () => ({ securityScannersEnabled: true }), focusedPeerSubject: () => null,
        focusedPeerViews: () => [], openStageWithPlacement, openCommand: vi.fn(), windowFocused: () => false });
      return null;
    });
    for (const destination of ["context.files", "settings.providers", "home", "chat", "composer", "sidebar", "all_chats"]) seams.effects.navigate(destination);
    expect(seams.invoke.mock.calls.map(([id]) => id)).toEqual(["go.context.files", "settings.section.providers", "go.home", "go.chat", "go.composer", "go.sidebar", "go.allChats"]);
    seams.effects.navigate("files"); expect(openStageWithPlacement).toHaveBeenCalledWith("files", "routed");
    seams.effects.composerPrefill("A new prompt"); expect(seams.prefill).toHaveBeenCalledWith("project", "A new prompt");
    setNav("chats"); setNav("files"); setNav("settings");
    view.unmount(); expect(seams.stop).toHaveBeenCalledOnce();
  });
});
