import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { Session } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { deferred } from "./session-lifecycle-test-fixture.ts";
import { beginSessionModeAdmission, rebaseSessionModeIntents, sessionModeView, setDraftSessionMode } from "./session-mode-state.ts";
import { upgradeSessionMode } from "./session-mode-upgrade.ts";

const session: Session = {
  id: "root", root_session_id: "root", project_id: "project", owner_person_id: "person",
  posture: "build", mode: "ask", started: true, status: "idle",
  created_at: "t", activity_at: "t", updated_at: "t",
};

describe("session mode intent", () => {
  it("hides until hydration and keeps the started Agent switch closed after rewind", () => {
    const store = createAppStore();
    expect(sessionModeView(store, session.id).show).toBe(false);
    store.actions.setCurrentSession({ ...session, mode: "agent" });
    store.actions.mergeSession({ id: session.id, status: "idle", current_turn: 0 });
    store.actions.setCurrentSession({ ...session, mode: "ask", started: false });
    expect(store.state.messages).toHaveLength(0);
    expect(sessionModeView(store, session.id)).toMatchObject({ show: false, mode: "agent", started: true });
  });

  it("freezes draft intent for overlapping first admissions and restores toggling after they end", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ ...session, mode: "agent", started: false });
    setDraftSessionMode(store, session.id, "ask");
    const first = beginSessionModeAdmission(store, session.id);
    setDraftSessionMode(store, session.id, "agent");
    const second = beginSessionModeAdmission(store, session.id);
    expect(first.mode).toBe("ask");
    expect(second.mode).toBe("ask");
    first.finish();
    expect(sessionModeView(store, session.id).admissionPending).toBe(true);
    second.finish();
    setDraftSessionMode(store, session.id, "agent");
    expect(sessionModeView(store, session.id)).toMatchObject({ mode: "agent", admissionPending: false });
  });

  it("preserves Ask selection across reconnect and isolates the old admission", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({ ...session, mode: "agent", started: false });
    setDraftSessionMode(store, session.id, "ask");
    const old = beginSessionModeAdmission(store, session.id);
    rebaseSessionModeIntents(store.actions);
    const current = beginSessionModeAdmission(store, session.id);
    old.finish();
    expect(current.mode).toBe("ask");
    expect(sessionModeView(store, session.id).admissionPending).toBe(true);
    current.finish();
  });

  it("joins a pending upgrade and keeps the request scoped when navigating away", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session);
    const response = deferred<Session>();
    const updateSession = vi.fn(() => response.promise);
    const client = stubClient({ updateSession });
    const upgrade = upgradeSessionMode(store, client, session.id);
    const join = upgradeSessionMode(store, client, session.id);

    expect(updateSession).toHaveBeenCalledExactlyOnceWith(session.id, { mode: "agent" });
    expect(sessionModeView(store, session.id)).toMatchObject({ mode: "ask", upgrade: "sending" });
    expect(beginSessionModeAdmission(store, session.id).mode).toBe("agent");
    const another = { ...session, id: "another", root_session_id: "another" };
    store.actions.setCurrentSession(another);
    expect(beginSessionModeAdmission(store, another.id).mode).toBeUndefined();
    response.resolve({ ...session, mode: "agent" });
    expect(await upgrade).toBe(true);
    expect(await join).toBe(true);
    expect(store.state.currentSession).toEqual(another);
  });

  it("reconciles after an uncertain transport result and keeps retry available before confirmation", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session);
    const updateSession = vi.fn().mockRejectedValue(new Error("Response lost"));
    const getSession = vi.fn().mockResolvedValueOnce(session).mockResolvedValueOnce({ ...session, mode: "agent" });
    const client = stubClient({ updateSession, getSession });
    expect(await upgradeSessionMode(store, client, session.id)).toBe(false);
    expect(sessionModeView(store, session.id)).toMatchObject({ mode: "ask", upgrade: "reconciling", show: true });
    expect(await upgradeSessionMode(store, client, session.id)).toBe(true);
    expect(sessionModeView(store, session.id)).toMatchObject({ mode: "agent", upgrade: undefined, show: false });
  });

  it.each([
    new LycaonApiError("Timed out", 408, "invalid_request"),
    new LycaonApiError("Proxy reply", 404, "internal_error"),
  ])("retains an ambiguous HTTP result for reconciliation", async (error) => {
    const store = createAppStore();
    store.actions.setCurrentSession(session);
    const getSession = vi.fn().mockResolvedValue(session);
    const client = stubClient({ updateSession: async () => { throw error; }, getSession });
    expect(await upgradeSessionMode(store, client, session.id)).toBe(false);
    expect(getSession).toHaveBeenCalledWith(session.id);
    expect(sessionModeView(store, session.id).upgrade).toBe("reconciling");
  });

  it("clears pending after a confirmed rejection and surfaces the host error", async () => {
    const store = createAppStore();
    store.actions.setCurrentSession(session);
    const error = new LycaonApiError("Denied", 403, "forbidden");
    const client = stubClient({ updateSession: async () => { throw error; } });
    await expect(upgradeSessionMode(store, client, session.id)).rejects.toBe(error);
    expect(sessionModeView(store, session.id)).toMatchObject({ mode: "ask", upgrade: undefined, show: true });
  });
});
