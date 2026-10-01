// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  parsePersistedSessionSnapshot,
  persistedSnapshotMatchesRevision,
  syncCachedProjectsToDisk,
  toSessionChatSnapshot,
  trimSnapshotForPersist,
} from "./session-chat-persist.ts";
import type { SessionChatSnapshot } from "./session-chat-snapshot.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import { wireProject } from "../../api/mocks/project-fixture.ts";
import {
  TRANSCRIPT_PAGE_BUDGET,
  TRANSCRIPT_PERSIST_TAIL_LIMIT,
} from "../transcript/layout/transcript-window.ts";

vi.mock("../../platform/connection/health.ts", () => ({
  lastSeenStoreRevision: () => 7,
}));

function sampleSnapshot(): SessionChatSnapshot {
  const messages = [{ id: "m1", role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "hi", seq: 1, created_at: "t" }];
  return {
    scope: { projectId: "p1", sessionId: "s1" },
    touchedAt: 1,
    session: {
      id: "s1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "p1",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    },
    transcript: {
      tail: messages,
      pages: {},
      hasTailGap: false,
      hasMoreBefore: false,
      hasMoreAfter: false,
    },
    messages,
    transcriptWatermark: 1,
    turnClocks: [],
    turnLoads: [],
    workers: [],
    workerTranscripts: {},
    workflowRuns: [],
    workflowCatalog: [],
    blueprints: [],
    pendingCheckpoints: [],
  };
}

describe("session-chat-persist", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests();
  });

  it("caps older pages to the page budget and stamps store revision", () => {
    const pages: Record<
      string,
      {
        id: string;
        role: "user";
        origin: "user";
        authority: "user";
        trust_tier: "trusted";
        content: string;
        seq: number;
        created_at: string;
        ord: number;
      }[]
    > = {};
    for (let i = 0; i < TRANSCRIPT_PAGE_BUDGET + 3; i++) {
      const key = `${i}:${i}`;
      pages[key] = [
        {
          id: `p${i}`,
          role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
          content: `page ${i}`,
          seq: i + 1,
          created_at: "t",
          ord: i,
        },
      ];
    }
    const base = sampleSnapshot();
    const persisted = trimSnapshotForPersist({
      ...base,
      transcript: {
        tail: base.transcript.tail,
        pages,
        hasTailGap: false,
        hasMoreBefore: true,
        hasMoreAfter: false,
      },
    });
    expect(Object.keys(persisted.transcript.pages)).toHaveLength(
      TRANSCRIPT_PAGE_BUDGET,
    );
    expect(persisted.transcript.hasTailGap).toBe(true);
    expect(persisted.storeRevisionAtPersist).toBe(7);
    expect(persisted).not.toHaveProperty("workerTranscripts");
  });

  it("rejects snapshots that lack the canonical transcript window", () => {
    expect(
      parsePersistedSessionSnapshot({
        ...trimSnapshotForPersist(sampleSnapshot()),
        transcript: undefined,
      }),
    ).toBeNull();
  });

  it("parses persisted snapshots and matches revision", () => {
    const persisted = trimSnapshotForPersist(sampleSnapshot());
    expect(parsePersistedSessionSnapshot(persisted)?.scope.sessionId).toBe("s1");
    setAppStateSnapshot({
      version: 1,
      recents: [],
      lastSessionSnapshot: persisted,
    });
    expect(persistedSnapshotMatchesRevision(7)).toBe(true);
    expect(persistedSnapshotMatchesRevision(8)).toBe(false);
  });

  it("accepts an explicit unknown revision only while the sidecar revision is unknown", () => {
    const persisted = {
      ...trimSnapshotForPersist(sampleSnapshot()),
      storeRevisionAtPersist: null,
    };
    setAppStateSnapshot({
      version: 1,
      recents: [],
      lastSessionSnapshot: persisted,
    });
    expect(persistedSnapshotMatchesRevision(null)).toBe(true);
    expect(persistedSnapshotMatchesRevision(1)).toBe(false);
  });

  it("rejects snapshots missing canonical persistence fields", () => {
    const persisted = trimSnapshotForPersist(sampleSnapshot());
    const { storeRevisionAtPersist: _, ...missingRevision } = persisted;
    expect(parsePersistedSessionSnapshot(missingRevision)).toBeNull();
    expect(
      parsePersistedSessionSnapshot({
        ...persisted,
        workflowRuns: undefined,
      }),
    ).toBeNull();
  });

  it("persists cached project summaries", async () => {
    await syncCachedProjectsToDisk([wireProject("/tmp/p", "p1")]);
    expect(getAppStateSnapshot().cachedProjects?.[0]?.id).toBe("p1");
  });
});

describe("persisted snapshot size", () => {
  const row = (id: string, seq: number) => ({
    id,
    seq,
    role: "assistant" as const,
    origin: "model" as const,
    authority: "system" as const,
    trust_tier: "trusted" as const,
    content: "x".repeat(64),
    created_at: "t",
  });

  const withTail = (tail: ReturnType<typeof row>[]): SessionChatSnapshot => ({
    ...sampleSnapshot(),
    transcript: {
      tail,
      pages: {},
      hasTailGap: false,
      hasMoreBefore: false,
      hasMoreAfter: false,
    },
    messages: tail,
  });

  it("bounds the tail and never writes the derivable message list", () => {
    const tail = Array.from({ length: TRANSCRIPT_PERSIST_TAIL_LIMIT + 50 }, (_, i) =>
      row(`m${i}`, i),
    );
    const persisted = trimSnapshotForPersist(withTail(tail));

    // Persistence caps the tail independently of live memory.
    expect(persisted.transcript.tail).toHaveLength(TRANSCRIPT_PERSIST_TAIL_LIMIT);
    expect(persisted.transcript.tail[TRANSCRIPT_PERSIST_TAIL_LIMIT - 1]?.id).toBe(
      tail[tail.length - 1]?.id,
    );
    expect(persisted.transcript.hasMoreBefore).toBe(true);
    expect("messages" in persisted).toBe(false);
  });

  it("records a gap when persistence trims a tail above retained pages", () => {
    const tail = Array.from(
      { length: TRANSCRIPT_PERSIST_TAIL_LIMIT + 1 },
      (_, index) => row(`m${index}`, index + 2),
    );
    const snapshot = withTail(tail);
    snapshot.transcript.pages = { "1:1": [{ ...row("older", 1), ord: 1 }] };

    expect(trimSnapshotForPersist(snapshot).transcript.hasTailGap).toBe(true);
  });

  it("restores the message list by deriving it from the window", () => {
    const tail = [row("a", 1), row("b", 2)];
    const persisted = trimSnapshotForPersist(withTail(tail));

    expect(toSessionChatSnapshot(persisted).messages.map((m) => m.id)).toEqual([
      "a",
      "b",
    ]);
  });
});
