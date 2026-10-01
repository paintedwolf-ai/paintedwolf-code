import { afterEach, describe, expect, it } from "vitest";
import {
  resetWorkspaceViewRegistryForTests,
  setCurrentWorkspaceContexts,
  setWorkspaceViewPresentationsForTests,
  workspaceContextEqual,
  workspaceContextIsOpen,
  type WorkspaceContext,
} from "./workspace-view-registry.ts";

const chat: WorkspaceContext = {
  kind: "session",
  projectId: "p1",
  sessionId: "s1",
};

afterEach(() => resetWorkspaceViewRegistryForTests());

describe("workspace view registry", () => {
  it("matches exact typed context identities", () => {
    expect(workspaceContextEqual(chat, { ...chat })).toBe(true);
    expect(
      workspaceContextEqual(chat, {
        kind: "session",
        projectId: "p1",
        sessionId: "s2",
      }),
    ).toBe(false);
    expect(
      workspaceContextEqual(
        { kind: "stage", projectId: "p1", stageId: "files" },
        { kind: "stage", projectId: "p2", stageId: "files" },
      ),
    ).toBe(false);
  });

  it("finds a context in the current window's split", () => {
    setCurrentWorkspaceContexts([
      chat,
      { kind: "stage", projectId: "p1", stageId: "files" },
    ]);
    expect(workspaceContextIsOpen(chat)).toBe(true);
  });

  it("finds a context in another window", () => {
    setWorkspaceViewPresentationsForTests([
      { label: "session:s1", viewId: "peer-1", contexts: [chat] },
    ]);
    expect(workspaceContextIsOpen(chat)).toBe(true);
  });

  it("ignores this view's stale host mirror after local navigation", () => {
    setCurrentWorkspaceContexts([
      { kind: "stage", projectId: "p1", stageId: "files" },
    ]);
    setWorkspaceViewPresentationsForTests([
      { label: "main", viewId: "main", contexts: [chat] },
    ]);
    expect(workspaceContextIsOpen(chat)).toBe(false);
  });
});
