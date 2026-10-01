import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import type { FindingsDigest, Session } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { refreshFindings } from "./findings-actions.ts";
import { errorOf, valueOf } from "../../store/load-state.ts";

function session(id: string): Session {
  return {
    id,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: "00000000-0000-4000-8000-000000000001",
    workspace_path: "/tmp/project",
    posture: "build",
    status: "idle",
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  };
}

describe("refreshFindings", () => {
  it("rejects an old request after an A-B-A navigation", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-a"));

    let resolveRequest!: (digest: FindingsDigest) => void;
    const client = stubClient({
      getSessionFindings: vi.fn(
        () =>
          new Promise<FindingsDigest>((resolve) => {
            resolveRequest = resolve;
          }),
      ),
    });

    const pending = refreshFindings(appStore, client, "session-a");
    appStore.actions.setCurrentSession(session("session-b"));
    appStore.actions.setCurrentSession(session("session-a"));
    appStore.actions.setFindings(
      "session-a",
      appStore.state.sessionViewEpoch,
      { findings: [], revision: 9 },
    );

    resolveRequest({ findings: [], revision: 1 });
    await pending;

    expect(valueOf(appStore.state.findings)?.revision).toBe(9);
  });

  it("records a failed read without discarding the last shown digest", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session("session-a"));
    appStore.actions.setFindings(
      "session-a",
      appStore.state.sessionViewEpoch,
      { findings: [], revision: 3 },
    );

    const client = stubClient({
      getSessionFindings: () => Promise.reject(new Error("sidecar restarting")),
    });
    await refreshFindings(appStore, client, "session-a");

    // The failure is visible; the last shown digest survives it.
    expect(errorOf(appStore.state.findings)).toBe("sidecar restarting");
    expect(valueOf(appStore.state.findings)?.revision).toBe(3);
  });
});
