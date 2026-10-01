import { render } from "@solidjs/testing-library";
import { createMemo, createRoot, onMount, Show } from "solid-js";
import { describe, expect, it } from "vitest";
import { useStageHandoff } from "../ui/surface-reveal.ts";
import { createAppStore } from "../store/app-state.ts";
import { createShellStore } from "../store/shell-store.ts";
import { SESSION_CREATE_PENDING_ID } from "../chat/session/session-scope.ts";
import {
  deriveStageScope,
  holdChatPresence,
  type ChatPresence,
} from "./stage-scope.ts";
import { resolveStageColumn } from "./stage-placement.ts";
import type { Session } from "../api/types.ts";

function session(id: string, projectId: string): Session {
  return { id, project_id: projectId, status: "idle" } as Session;
}

type Stores = {
  shell: ReturnType<typeof createShellStore>;
  appStore: ReturnType<typeof createAppStore>;
};

function stores(): Stores {
  return { shell: createShellStore("connected"), appStore: createAppStore() };
}

/** Shell's split-column derivation — call within a reactive root. */
function splitColumn({ shell, appStore }: Stores) {
  const scope = createMemo(() =>
    deriveStageScope(shell.state, appStore.state),
  );
  const presence = createMemo((prev: ChatPresence | undefined) =>
    holdChatPresence(prev, scope()),
  );
  return createMemo(() =>
    resolveStageColumn({
      navStage: null,
      foregroundIsChat: true,
      hasChat: presence().present,
      companion: "files",
      splitMode: true,
    }),
  );
}

function splitHarness() {
  const s = stores();
  return { ...s, column: splitColumn(s) };
}

const LIVE = { splitLive: true, stageId: "files" } as const;

describe("split liveness across a chat switch", () => {
  it("holds the companion stage while the next chat in the project hydrates", () => {
    createRoot((dispose) => {
      const { shell, appStore, column } = splitHarness();

      shell.beginStageSwitch({ projectId: "p1", kind: "same-project" });
      shell.commitStageScope({ projectId: "p1", sessionId: "s1" });
      appStore.actions.setCurrentSession(session("s1", "p1"));
      expect(column()).toEqual(LIVE);

      // runResumeSession, same-project: lock, then move the foreground.
      appStore.actions.beginSessionResumeSwitch("s2");
      expect(column()).toEqual(LIVE);
      shell.commitStageScope({ projectId: "p1", sessionId: "s2" });
      expect(column()).toEqual(LIVE);
      appStore.actions.setCurrentSession(session("s2", "p1"));
      appStore.actions.completeChatSessionHydration();
      expect(column()).toEqual(LIVE);

      dispose();
    });
  });

  it("holds it across a new chat, whose transcript clear has no ready session", () => {
    createRoot((dispose) => {
      const { shell, appStore, column } = splitHarness();

      shell.commitStageScope({ projectId: "p1", sessionId: "s1" });
      appStore.actions.setCurrentSession(session("s1", "p1"));
      expect(column()).toEqual(LIVE);

      // runCreateSession: wildcard clear, pending foreground, then the server row.
      appStore.actions.clearChatForSessionSwitch("*");
      expect(column()).toEqual(LIVE);
      shell.commitStageScope({
        projectId: "p1",
        sessionId: SESSION_CREATE_PENDING_ID,
      });
      expect(column()).toEqual(LIVE);
      shell.commitStageScope({ projectId: "p1", sessionId: "s2" });
      appStore.actions.setCurrentSession(session("s2", "p1"));
      appStore.actions.completeChatSessionHydration();
      expect(column()).toEqual(LIVE);

      dispose();
    });
  });

  it("does not carry a project's split into another project", () => {
    createRoot((dispose) => {
      const { shell, appStore, column } = splitHarness();

      shell.commitStageScope({ projectId: "p1", sessionId: "s1" });
      appStore.actions.setCurrentSession(session("s1", "p1"));
      expect(column()).toEqual(LIVE);

      shell.beginStageSwitch({ projectId: "p2", kind: "cross-project" });
      appStore.actions.clearChatForSessionSwitch("*");
      expect(column()).toEqual({ splitLive: false, stageId: null });

      dispose();
    });
  });

  it("does not remount the stage pane when the conversation beside it changes", () => {
    const { shell, appStore } = stores();
    let mounts = 0;

    shell.commitStageScope({ projectId: "p1", sessionId: "s1" });
    appStore.actions.setCurrentSession(session("s1", "p1"));

    // The stage stays mounted while the chat prepares.
    render(() => {
      const column = splitColumn({ shell, appStore });
      const handoff = useStageHandoff(
        () => column().stageId,
        () => null,
        () => false,
      );
      return (
        <Show when={handoff()} keyed>
          {(stage) => {
            onMount(() => {
              mounts += 1;
            });
            return <div data-testid="stage-pane">{stage}</div>;
          }}
        </Show>
      );
    });
    expect(mounts).toBe(1);

    // Resume another chat.
    appStore.actions.beginSessionResumeSwitch("s2");
    shell.commitStageScope({ projectId: "p1", sessionId: "s2" });
    appStore.actions.setCurrentSession(session("s2", "p1"));
    appStore.actions.completeChatSessionHydration();
    expect(mounts).toBe(1);

    // Start a new one.
    appStore.actions.clearChatForSessionSwitch("*");
    shell.commitStageScope({
      projectId: "p1",
      sessionId: SESSION_CREATE_PENDING_ID,
    });
    shell.commitStageScope({ projectId: "p1", sessionId: "s3" });
    appStore.actions.setCurrentSession(session("s3", "p1"));
    appStore.actions.completeChatSessionHydration();
    expect(mounts).toBe(1);
  });

  it("drops the split when the foreground chat goes away", () => {
    createRoot((dispose) => {
      const { shell, appStore, column } = splitHarness();

      shell.commitStageScope({ projectId: "p1", sessionId: "s1" });
      appStore.actions.setCurrentSession(session("s1", "p1"));
      expect(column()).toEqual(LIVE);

      shell.evictConversation("p1", "s1");
      expect(column()).toEqual({ splitLive: false, stageId: null });

      dispose();
    });
  });
});
