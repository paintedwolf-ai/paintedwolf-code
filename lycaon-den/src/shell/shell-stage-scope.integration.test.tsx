import { render, screen } from "@solidjs/testing-library";
import { Show } from "solid-js";
import { describe, expect, it } from "vitest";
import { createAppStore } from "../store/app-state.ts";
import { createShellStore } from "../store/shell-store.ts";
import { createProjectsStore } from "../store/projects-store.ts";
import { deriveStageScope } from "./stage-scope.ts";
import { ProjectLoadingStage } from "../components/shell/ProjectLoadingStage.tsx";
import type { Project, ProjectEvent, Session } from "../api/types.ts";

function StageGate(props: {
  shell: ReturnType<typeof createShellStore>;
  appStore: ReturnType<typeof createAppStore>;
}) {
  const scope = () =>
    deriveStageScope(props.shell.state, props.appStore.state);

  return (
    <>
      <Show when={scope().phase === "home"}>
        <div data-testid="home-view" />
      </Show>
      <Show when={scope().phase === "opening" || scope().phase === "switching"}>
        <ProjectLoadingStage />
      </Show>
      <Show when={scope().phase === "ready"}>
        <div
          data-testid="chat-view"
          data-project-id={scope().project_id ?? ""}
          data-session-id={scope().session_id ?? ""}
        />
      </Show>
    </>
  );
}

describe("shell stage scope across a cross-project launch", () => {
  it("does not mount ChatView with stale project while intent leads foreground", () => {
    const appStore = createAppStore();

    appStore.actions.setCurrentSession({
      id: "sess-a",
      project_id: "proj-a",
      status: "idle",
    } as Session);
    appStore.actions.installTranscriptBaseline(
      "sess-a",
      [{ id: "stale", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "wrong repo", created_at: "t" }],
      0,
    );

    const scope = deriveStageScope(
      {
        activeProjectId: "proj-b",
        foreground: { projectId: "proj-a", sessionId: "sess-a" },
        opening: null,
      },
      appStore.state,
    );
    expect(scope.phase).toBe("switching");
    expect(scope.project_id).toBe("proj-b");
    expect(scope.session_id).toBeNull();

    render(() => (
      <>
        <Show when={scope.phase === "switching"}>
          <ProjectLoadingStage />
        </Show>
        <Show when={scope.phase === "ready"}>
          <div data-testid="chat-view" data-project-id={scope.project_id ?? ""} />
        </Show>
      </>
    ));
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy();
    expect(screen.queryByTestId("chat-view")).toBeNull();
  });

  it("shows loading stage during cross-project wildcard hydration lock", () => {
    const shell = createShellStore("connected");
    const appStore = createAppStore();

    shell.beginStageSwitch({ projectId: "proj-b", kind: "cross-project" });
    shell.commitStageScope({ projectId: "proj-b", sessionId: "sess-b" });
    appStore.actions.clearChatForSessionSwitch("*");

    const scope = deriveStageScope(shell.state, appStore.state);
    expect(scope.phase).toBe("switching");

    render(() => <StageGate shell={shell} appStore={appStore} />);
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy();
    expect(screen.queryByTestId("chat-view")).toBeNull();
  });

  it("never paints Home between the outgoing project and a project being created", () => {
    const shell = createShellStore("connected");
    const appStore = createAppStore();
    let projectEvent: ((ev: ProjectEvent) => void) | undefined;
    const store = createProjectsStore(() => null, {
      onProjectEvent: (cb) => {
        projectEvent = cb;
        return () => undefined;
      },
    });

    shell.commitStageScope({ projectId: "proj-a", sessionId: "sess-a" });
    appStore.actions.setCurrentSession({
      id: "sess-a",
      project_id: "proj-a",
      status: "idle",
    } as Session);
    render(() => <StageGate shell={shell} appStore={appStore} />);
    expect(screen.getByTestId("chat-view")).toBeTruthy();

    // The user asked for a new project: the veil is up before any request.
    appStore.actions.clearChatForSessionSwitch("*");
    const token = shell.beginWorkspaceOpening({ label: "Untitled" });
    expect(deriveStageScope(shell.state, appStore.state).phase).toBe("opening");
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy();
    expect(screen.queryByTestId("home-view")).toBeNull();
    expect(screen.queryByTestId("chat-view")).toBeNull();

    // The sidecar announces the record before the create response resolves.
    projectEvent?.({
      id: "proj-new",
      action: "created",
      project: {
        id: "proj-new",
        name: "",
        roots: [],
        is_draft: true,
      } as unknown as Project,
    });
    expect(store.byId("proj-new")).toBeTruthy();
    expect(screen.queryByTestId("home-view")).toBeNull();
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy();

    // The response lands; the switch proper replaces the opening.
    shell.beginStageSwitch({ projectId: "proj-new", kind: "cross-project" });
    expect(shell.state.opening).toBeNull();
    expect(deriveStageScope(shell.state, appStore.state).phase).toBe("switching");
    expect(screen.getByTestId("project-loading-stage")).toBeTruthy();
    expect(screen.queryByTestId("home-view")).toBeNull();

    // A stale abandon from the superseded open changes nothing.
    shell.abandonWorkspaceOpening(token);
    expect(shell.state.activeProjectId).toBe("proj-new");
  });

  it("returns an abandoned open to Home", () => {
    const shell = createShellStore("connected");
    const appStore = createAppStore();
    render(() => <StageGate shell={shell} appStore={appStore} />);

    const token = shell.beginWorkspaceOpening({ label: "widgets" });
    expect(screen.queryByTestId("home-view")).toBeNull();
    shell.abandonWorkspaceOpening(token);
    expect(deriveStageScope(shell.state, appStore.state).phase).toBe("home");
    expect(screen.getByTestId("home-view")).toBeTruthy();
    expect(screen.queryByTestId("project-loading-stage")).toBeNull();
  });

  it("mounts ChatView only when scope is ready and coherent", () => {
    const shell = createShellStore("connected");
    const appStore = createAppStore();

    shell.commitStageScope({ projectId: "proj-a", sessionId: "sess-a" });
    appStore.actions.setCurrentSession({
      id: "sess-a",
      project_id: "proj-a",
      status: "idle",
    } as Session);

    const scope = deriveStageScope(shell.state, appStore.state);
    expect(scope.phase).toBe("ready");

    render(() => <StageGate shell={shell} appStore={appStore} />);
    const chat = screen.getByTestId("chat-view");
    expect(chat.getAttribute("data-project-id")).toBe("proj-a");
    expect(chat.getAttribute("data-session-id")).toBe("sess-a");
    expect(screen.queryByTestId("project-loading-stage")).toBeNull();
  });
});
