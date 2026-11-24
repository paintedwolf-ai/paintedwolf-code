import { describe, expect, it } from "vitest";
import { createShellStore } from "./shell-store.ts";

describe("shell-store", () => {
  it("commitStageScope binds active project and foreground", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    expect(store.state.activeProjectId).toBe("p1");
    expect(store.state.foreground).toEqual({ projectId: "p1", sessionId: "s1" });
    expect(store.state.launch).toBeNull();
  });

  it("shows a Home launch as the foreground conversation before any project exists", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    store.openLaunch("launch-1");
    expect(store.state).toMatchObject({
      activeProjectId: null,
      foreground: null,
      launch: { launchId: "launch-1" },
      opening: null,
    });
  });

  it("any other navigation replaces the launch foreground", () => {
    const store = createShellStore("connected");
    store.openLaunch("launch-1");
    store.beginStageSwitch({ projectId: "p2", kind: "cross-project" });
    expect(store.state.launch).toBeNull();

    store.openLaunch("launch-1");
    store.clearToHome();
    expect(store.state.launch).toBeNull();

    store.openLaunch("launch-1");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    expect(store.state.launch).toBeNull();
  });

  it("closing a launch leaves newer navigation alone", () => {
    const store = createShellStore("connected");
    store.openLaunch("launch-1");
    store.openLaunch("launch-2");
    store.closeLaunch("launch-1");
    expect(store.state.launch).toEqual({ launchId: "launch-2" });
    store.closeLaunch("launch-2");
    expect(store.state.launch).toBeNull();
  });

  it("keeps an initial Home prompt with the foreground session", () => {
    const store = createShellStore("offline");
    store.commitStageScope({
      projectId: "p1",
      sessionId: "s1",
      initialPrompt: true,
    });
    expect(store.state.foreground).toEqual({
      projectId: "p1",
      sessionId: "s1",
      initialPrompt: true,
    });
  });

  it("beginStageSwitch same-project keeps foreground", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    store.beginStageSwitch({ projectId: "p1", kind: "same-project" });
    expect(store.state.activeProjectId).toBe("p1");
    expect(store.state.foreground).toEqual({ projectId: "p1", sessionId: "s1" });
  });

  it("beginStageSwitch cross-project evicts stale foreground", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    store.beginStageSwitch({ projectId: "p2", kind: "cross-project" });
    expect(store.state.activeProjectId).toBe("p2");
    expect(store.state.foreground).toBeNull();
  });

  it("clearToHome drops active project and foreground", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    store.clearToHome();
    expect(store.state.activeProjectId).toBeNull();
    expect(store.state.foreground).toBeNull();
  });

  it("opens a workspace ahead of its project id without touching the restore target", () => {
    const store = createShellStore("connected");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    const token = store.beginWorkspaceOpening({ label: "widgets" });
    expect(store.state).toMatchObject({
      activeProjectId: null,
      foreground: null,
      materializingDraft: false,
      opening: { token, label: "widgets" },
    });
  });

  it("hands the opening to the switch that names the project", () => {
    const store = createShellStore("connected");
    store.beginWorkspaceOpening({ label: "widgets" });
    store.beginStageSwitch({ projectId: "p2", kind: "cross-project" });
    expect(store.state.opening).toBeNull();
    expect(store.state.activeProjectId).toBe("p2");

    store.beginWorkspaceOpening({ label: "gadgets" });
    store.commitStageScope({ projectId: "p3", sessionId: "s3" });
    expect(store.state.opening).toBeNull();
    expect(store.state.activeProjectId).toBe("p3");
  });

  it("abandons only the open matching the token", () => {
    const store = createShellStore("connected");
    const first = store.beginWorkspaceOpening({ label: "first" });
    const second = store.beginWorkspaceOpening({ label: "second" });
    expect(second).toBeGreaterThan(first);

    store.abandonWorkspaceOpening(first);
    expect(store.state.opening).toEqual({ token: second, label: "second" });

    store.abandonWorkspaceOpening(second);
    expect(store.state).toMatchObject({
      activeProjectId: null,
      foreground: null,
      opening: null,
    });
  });

  it("Home recovery and a launch leave no opening behind", () => {
    const store = createShellStore("connected");
    store.beginWorkspaceOpening({ label: "widgets" });
    store.clearToHome();
    expect(store.state.opening).toBeNull();

    store.beginWorkspaceOpening({ label: "widgets" });
    store.openLaunch("launch-1");
    expect(store.state.opening).toBeNull();
  });

  it("evicting active project clears active foreground", () => {
    const store = createShellStore("offline");
    store.commitStageScope({ projectId: "p1", sessionId: "s1" });
    store.evictProjectConversations("p1");
    expect(store.state.activeProjectId).toBeNull();
    expect(store.state.foreground).toBeNull();
  });
});
