import { describe, expect, it, vi } from "vitest";
import type { RecentSession } from "../../../shared/app-state-types.ts";
import {
  deleteSession,
  moveSessionPin,
  retireSessionLocally,
  setSessionArchived,
  setSessionPinned,
  type LocalRetirementDeps,
  type SessionRetirementDeps,
} from "./session-retirement.ts";

const target = {
  projectId: "proj-1",
  sessionId: "sess-1",
  title: "Chat",
};

function fixture(overrides: Record<string, unknown> = {}) {
  const client = {
    updateSession: vi.fn().mockResolvedValue({}),
    deleteSession: vi.fn().mockResolvedValue(undefined),
    ...overrides,
  };
  const deps: SessionRetirementDeps = {
    client: () => client as never,
    reportError: vi.fn(),
    removeProjectRow: vi.fn(),
    applyProjectRowPatches: vi.fn(async (_patches, send) => {
      await send();
    }),
    nextPinRank: () => 3,
    pinnedIds: () => ["pin-a", "sess-1", "pin-b"],
    refreshProjectRows: vi.fn(),
    retireLocally: vi.fn().mockResolvedValue(undefined),
  };
  return { client, deps };
}

describe("session retirement", () => {
  it("does not retire local state when archive fails", async () => {
    const error = new Error("archive failed");
    const { deps } = fixture({ updateSession: vi.fn().mockRejectedValue(error) });

    await expect(setSessionArchived(deps, target, true)).resolves.toBe(false);

    expect(deps.reportError).toHaveBeenCalledWith(error, target);
    expect(deps.removeProjectRow).not.toHaveBeenCalled();
    expect(deps.refreshProjectRows).not.toHaveBeenCalled();
    expect(deps.retireLocally).not.toHaveBeenCalled();
  });

  it("retires archive and delete only after the host succeeds", async () => {
    const archived = fixture();
    await expect(setSessionArchived(archived.deps, target, true)).resolves.toBe(true);
    expect(archived.deps.retireLocally).toHaveBeenCalledWith(target);

    const deleted = fixture();
    await expect(deleteSession(deleted.deps, target)).resolves.toBe(true);
    expect(deleted.deps.retireLocally).toHaveBeenCalledWith(target);
  });

  it("pins through the sidebar row's pending edit, then refreshes", async () => {
    const { client, deps } = fixture();

    await expect(setSessionPinned(deps, target, true)).resolves.toBe(true);

    expect(deps.applyProjectRowPatches).toHaveBeenCalledWith(
      new Map([["sess-1", { pin_rank: 3 }]]),
      expect.any(Function),
    );
    expect(client.updateSession).toHaveBeenCalledWith("sess-1", { pinned: true });
    expect(deps.refreshProjectRows).toHaveBeenCalledOnce();
    expect(deps.retireLocally).not.toHaveBeenCalled();
  });

  it("unpins by clearing the provisional rank", async () => {
    const { client, deps } = fixture();

    await expect(setSessionPinned(deps, target, false)).resolves.toBe(true);

    expect(deps.applyProjectRowPatches).toHaveBeenCalledWith(
      new Map([["sess-1", { pin_rank: undefined }]]),
      expect.any(Function),
    );
    expect(client.updateSession).toHaveBeenCalledWith("sess-1", { pinned: false });
  });

  it("moves a pin by renumbering the shown order", async () => {
    const { client, deps } = fixture();

    await expect(moveSessionPin(deps, target, 3)).resolves.toBe(true);

    expect(deps.applyProjectRowPatches).toHaveBeenCalledWith(
      new Map([
        ["pin-a", { pin_rank: 1 }],
        ["pin-b", { pin_rank: 2 }],
        ["sess-1", { pin_rank: 3 }],
      ]),
      expect.any(Function),
    );
    expect(client.updateSession).toHaveBeenCalledWith("sess-1", { pin_position: 3 });
    expect(deps.refreshProjectRows).toHaveBeenCalledOnce();
  });

  it("does not move a chat that is not pinned", async () => {
    const { client, deps } = fixture();

    await expect(moveSessionPin(deps, { ...target, sessionId: "loose" }, 1)).resolves.toBe(false);

    expect(client.updateSession).not.toHaveBeenCalled();
  });

  it("reports a failed pin without refreshing", async () => {
    const error = new Error("pin failed");
    const { deps } = fixture({ updateSession: vi.fn().mockRejectedValue(error) });

    await expect(setSessionPinned(deps, target, true)).resolves.toBe(false);

    expect(deps.reportError).toHaveBeenCalledWith(error, target);
    expect(deps.refreshProjectRows).not.toHaveBeenCalled();
  });
});

describe("retireSessionLocally", () => {
  function localFixture(active: { projectId: string; sessionId: string } | null) {
    const recents: RecentSession[] = [
      { projectId: "proj-1", sessionId: "sess-1", title: "Chat" },
      { projectId: "proj-1", sessionId: "sess-2", title: "Next" },
    ];
    const deps: LocalRetirementDeps = {
      activeChat: () => active,
      recents: { state: { recents, loaded: true } },
      retireEntity: vi.fn(),
      resumeSession: vi.fn().mockResolvedValue(undefined),
      evictConversation: vi.fn(),
    };
    return deps;
  }

  it("retires the open chat, then moves to the next recent chosen before retirement", async () => {
    const deps = localFixture({ projectId: "proj-1", sessionId: "sess-1" });

    await retireSessionLocally(deps, target);

    expect(deps.retireEntity).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-1", sessionId: "sess-1" }),
    );
    expect(deps.resumeSession).toHaveBeenCalledWith(
      expect.objectContaining({ sessionId: "sess-2" }),
      { keepStage: true },
    );
  });

  it("evicts the last project chat without navigating to another project's recents", async () => {
    const deps = localFixture({ projectId: "proj-1", sessionId: "sess-1" });
    deps.recents.state.recents = [
      target,
      { projectId: "proj-2", sessionId: "sess-3", title: "Other project" },
    ];
    await retireSessionLocally(deps, target);
    expect(deps.resumeSession).not.toHaveBeenCalled();
    expect(deps.evictConversation).toHaveBeenCalledWith("proj-1", "sess-1");
  });

  it("leaves the selection alone when a background chat retires", async () => {
    const deps = localFixture({ projectId: "proj-1", sessionId: "sess-2" });

    await retireSessionLocally(deps, target);

    expect(deps.resumeSession).not.toHaveBeenCalled();
    expect(deps.evictConversation).not.toHaveBeenCalled();
  });
});
