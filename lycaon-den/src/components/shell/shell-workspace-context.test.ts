import { createRoot } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ActiveChat } from "../../shell/stage-scope.ts";
import type { Nav } from "./shell-navigation-state.ts";
import { createShellWorkspaceContext } from "./shell-workspace-context.ts";

const registry = vi.hoisted(() => ({
  visible: false,
  collapsed: false,
  source: null as { projectId: string; path: string; line: number } | null,
  placement: "titlebar" as "titlebar" | "stage",
}));
const sourceReturns = vi.hoisted(() => ({ clear: vi.fn(), returnTo: vi.fn(async () => true) }));
vi.mock("../../files/source/source-return.ts", () => ({
  sourceReturn: () => registry.source,
  clearSourceReturn: sourceReturns.clear,
  returnToSource: sourceReturns.returnTo,
}));
vi.mock("../../platform/windows/workspace-view-registry.ts", () => ({
  workspaceContextEqual: (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b),
  workspaceContextIsOpen: () => registry.visible,
}));
vi.mock("../../shell/layout-store.ts", () => ({ hiddenSplitPanePref: () => registry.collapsed ? "conversation" : null }));
vi.mock("../stage/stage-registry.tsx", () => ({
  stageLabelFor: (id: string) => id,
  stageDefinition: () => ({ backPlacement: registry.placement }),
}));

function fixture() {
  let chat: ActiveChat | null = { projectId: "project", sessionId: "previous" };
  let foreground: Nav = "files";
  let conversation = true;
  const showConversation = vi.fn(async () => true);
  const claim = vi.fn();
  const readProject = vi.fn(() => "project");
  const closeWorkers = vi.fn();
  const reopenFiles = vi.fn();
  const controller = createRoot(() => createShellWorkspaceContext({
    activeProjectId: readProject,
    presentedChat: () => chat,
    activeChat: () => ({ projectId: "project", sessionId: "next" }),
    projectPresentation: () => ({ projectId: "project", opening: false, ready: { identity: true, mounted: true, open: true } }),
    conversationShown: () => conversation,
    splitColumns: () => ({ conversation, stage: true }),
    stageColumnStageId: () => "files",
    nav: () => foreground,
    setNav: (value) => { foreground = value; },
    routedReturn: () => ({ stage: "files", from: "projects" }),
    splitLive: () => true,
    showConversation,
    splitFitClaim: { claim },
    stageHandoff: () => "files",
    closeWorkers,
    reopenFiles,
  }));
  return {
    controller, readProject, showConversation, claim, closeWorkers, reopenFiles,
    setPresented: (value: ActiveChat | null) => { chat = value; },
    hideConversation: () => { conversation = false; },
    foreground: () => foreground,
  };
}

describe("shell workspace context", () => {
  beforeEach(() => {
    registry.visible = false;
    registry.collapsed = false;
    registry.source = null;
    registry.placement = "titlebar";
  });

  it("reads current presentation lazily and keeps the outgoing chat visible during handoff", () => {
    const f = fixture();
    expect(f.readProject).not.toHaveBeenCalled();
    expect(f.controller.shownWorkspaceContexts()).toEqual([
      { kind: "session", projectId: "project", sessionId: "previous" },
      { kind: "stage", projectId: "project", stageId: "files" },
    ]);
    f.setPresented(null);
    expect(f.controller.shownWorkspaceContexts()[0]).toEqual({ kind: "session", projectId: "project", sessionId: "next" });
  });

  it("omits hidden conversation context and claims room when returning to it", () => {
    const f = fixture();
    f.hideConversation();
    expect(f.controller.shownWorkspaceContexts()).toEqual([{ kind: "stage", projectId: "project", stageId: "files" }]);
    f.controller.backFor("files", "files-back")!.onBack();
    expect(f.foreground()).toBe("projects");
    expect(f.claim).toHaveBeenCalledOnce();
    expect(f.showConversation).not.toHaveBeenCalled();
  });

  it("restores a collapsed conversation before considering a width claim", () => {
    const f = fixture();
    f.hideConversation();
    registry.collapsed = true;
    f.controller.backFor("files", "files-back")!.onBack();
    expect(f.showConversation).toHaveBeenCalledOnce();
    expect(f.claim).not.toHaveBeenCalled();
  });

  it("hides routed return when its origin is already visible in another view", () => {
    const f = fixture();
    registry.visible = true;
    expect(f.controller.backFor("files", "files-back")).toBeNull();
    expect(f.controller.backFor("settings", "settings-back")).toBeNull();
  });

  it("places each stage's back chip where the registry declares it", () => {
    const f = fixture();
    expect(f.controller.titlebarBack()).toMatchObject({ testId: "files-back" });
    expect(f.controller.stageBack("files")).toBeNull();
    registry.placement = "stage";
    expect(f.controller.titlebarBack()).toBeNull();
    expect(f.controller.stageBack("files")).toMatchObject({ testId: "files-back" });
  });

  it("lets a source return outrank every stage back chip and reopen Files once it lands", async () => {
    const f = fixture();
    registry.source = { projectId: "project", path: "src/a.ts", line: 4 };
    const back = f.controller.titlebarBack();
    expect(back).toMatchObject({ label: "Back to src/a.ts:4", testId: "line-facts-back" });
    registry.placement = "stage";
    expect(f.controller.stageBack("files")).toBeNull();
    back!.onBack();
    expect(f.closeWorkers).toHaveBeenCalledOnce();
    await vi.waitFor(() => expect(f.reopenFiles).toHaveBeenCalledOnce());
  });

  it("offers no source return for another project", () => {
    const f = fixture();
    registry.source = { projectId: "other", path: "src/a.ts", line: 4 };
    expect(f.controller.sourceBack()).toBeNull();
  });
});
