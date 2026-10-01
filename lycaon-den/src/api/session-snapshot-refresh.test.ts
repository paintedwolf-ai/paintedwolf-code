import { describe, expect, it } from "vitest";
import { createAppStore } from "../store/app-state.ts";
import { stubClient } from "../test/client-fixture.ts";
import type { Session } from "./types.ts";
import { beginSessionSnapshotRead, refreshSessionSnapshot, resetSessionSnapshotReads } from "./session-snapshot-refresh.ts";

function fixture() {
  const store = createAppStore();
  const session: Session = {
    id: "session", owner_person_id: "00000000-0000-4000-8000-000000000002", project_id: "project", status: "busy", posture: "build",
    workspace_path: "/project", created_at: "t", activity_at: "t", updated_at: "t",
  };
  store.actions.setCurrentSession(session);
  let finish!: (value: Session) => void;
  const response = new Promise<Session>((resolve) => { finish = resolve; });
  return { store, session, finish, client: stubClient({ getSession: () => response }) };
}

describe("session snapshot refresh", () => {
  it.each(["older first", "newer first"])("keeps the latest mutation projection when responses finish %s", (order) => {
    const { store, session } = fixture();
    const older = beginSessionSnapshotRead(store, session.id);
    const newer = beginSessionSnapshotRead(store, session.id);
    const finish = (read: ReturnType<typeof beginSessionSnapshotRead>, title: string) => {
      if (read.isCurrent()) store.actions.setSessionTitle(session.id, title);
      read.finish();
    };
    if (order === "older first") {
      finish(older, "Old");
      expect(store.state.currentSession?.title).toBeUndefined();
      finish(newer, "New");
    } else {
      finish(newer, "New");
      finish(older, "Old");
    }
    expect(store.state.currentSession).toMatchObject({ title: "New", status: "busy" });
  });

  it("reports an idle read without settling a prompt the host has not started", async () => {
    const { store, session, finish, client } = fixture();
    store.actions.mergeSession({ ...session, status: "idle" });
    store.actions.holdPromptSubmission(session.id, "submission-1");

    const read = refreshSessionSnapshot(store, client, session.id);
    finish({ ...session, status: "idle", title: "Read before the turn began" });
    await read;

    expect(store.state.currentSession).toMatchObject({ status: "idle", title: "Read before the turn began" });
    expect(store.state.sessionActivity[session.id]?.promptSubmissions).toEqual([{ id: "submission-1" }]);
  });

  it("ignores an older read that finishes after a newer read", async () => {
    const { store, session, finish, client } = fixture();
    const older = refreshSessionSnapshot(store, client, session.id);
    await refreshSessionSnapshot(store, stubClient({
      getSession: async () => ({ ...session, status: "idle", title: "New" }),
    }), session.id);
    finish({ ...session, title: "Old" });
    await older;
    expect(store.state.currentSession).toMatchObject({ status: "idle", title: "New" });
  });

  it("ignores a read after switching away and back to the same session", async () => {
    const { store, session, finish, client } = fixture();
    const old = refreshSessionSnapshot(store, client, session.id);
    store.actions.setCurrentSession({ ...session, id: "other" });
    store.actions.setCurrentSession({ ...session, status: "idle", title: "Reopened" });

    finish({ ...session, title: "Old" });
    await old;

    expect(store.state.currentSession).toMatchObject({ status: "idle", title: "Reopened" });
  });

  it("ignores a read from an earlier backend connection", async () => {
    const { store, session, finish, client } = fixture();
    const old = refreshSessionSnapshot(store, client, session.id);
    resetSessionSnapshotReads(store.actions);
    store.actions.mergeSession({ ...session, status: "idle", title: "Restarted" });
    finish({ ...session, title: "Old" });
    await old;
    expect(store.state.currentSession).toMatchObject({ status: "idle", title: "Restarted" });
  });

  it("ignores a read after its polling operation settles", async () => {
    const { store, session, finish, client } = fixture();
    let active = true;
    const old = refreshSessionSnapshot(store, client, session.id, () => active);
    active = false;
    finish({ ...session, status: "idle", title: "Late" });
    await old;
    expect(store.state.currentSession).toMatchObject({ status: "busy" });
    expect(store.state.currentSession?.title).toBeUndefined();
  });
});
