import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import type { Message } from "../../api/types.ts";
import {
  pendingAttachmentsForSession,
  resetComposerAttachmentsForTests,
} from "../composer/composer-attachment-store.ts";
import {
  composerDraftForSession,
  resetComposerDraftsForTests,
} from "../composer/composer-drafts.ts";
import { resetComposerDocumentStoreForTests } from "../composer/composer-document-store.ts";
import { isRewindAnchor, lastRewindAnchorId, runRewind } from "./session-recovery.ts";
import { pendingAttachmentsFromRestore } from "../transcript/content/restore-attachments.ts";

vi.mock("../session/session-transcript-hydrate.ts", () => ({
  applySessionTranscriptSnapshot: vi.fn(async () => undefined),
}));

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  isTauriRuntime: () => false,
}));

function msg(over: Partial<Message>): Message {
  return {
    id: "m",
    role: "user",
    content: "",
    ts: "2026-07-25T00:00:00Z",
    ...over,
  } as Message;
}

describe("rewind anchor eligibility", () => {
  it("accepts only visible user asks", () => {
    expect(isRewindAnchor(msg({ role: "user" }))).toBe(true);
    // Assistant replies are not rewind anchors.
    expect(isRewindAnchor(msg({ role: "assistant" }))).toBe(false);
    // Internal messages are not user asks.
    expect(isRewindAnchor(msg({ role: "user", visibility: "internal" }))).toBe(false);
  });

  it("picks the newest ask as the one that shows Edit", () => {
    const messages = [
      msg({ id: "u1", role: "user" }),
      msg({ id: "a1", role: "assistant" }),
      msg({ id: "u2", role: "user" }),
      msg({ id: "kick", role: "user", visibility: "internal" }),
    ];
    expect(lastRewindAnchorId(messages)).toBe("u2");
  });

  it("has no anchor in a chat with no asks yet", () => {
    expect(lastRewindAnchorId([msg({ id: "a1", role: "assistant" })])).toBeNull();
  });
});

describe("runRewind", () => {
  const appStore = {
    state: { pendingSends: {} },
    actions: { removePendingSends: vi.fn() },
  } as never;

  beforeEach(() => {
    resetComposerDraftsForTests();
    resetComposerAttachmentsForTests();
    resetComposerDocumentStoreForTests();
  });

  function fakeClient(
    restored = "the original ask",
    extra: Partial<{
      restored_content_parts: Message["content_parts"];
      restored_artifact_ids: string[];
    }> = {},
  ) {
    const client = {
      rewindSession: vi.fn(async () => ({
        restored_paths: ["src/foo.go"],
        truncated_message_count: 2,
        restored_prompt: restored,
        ...extra,
      })),
      listSessionMessages: vi.fn(async () => ({ messages: [], watermark: 0 })),
    };
    return client as typeof client & LycaonClient;
  }

  it("edit puts the restored ask in the composer draft", async () => {
    const client = fakeClient("rewrite the greeting");
    await runRewind(
      {
        client,
        appStore,
        projectId: "p1",
        sessionId: "s1",
        projectDir: "/p",
        projects: [],
      },
      { operationId: "operation-1", action: "edit", messageId: "u1", text: "stale local copy" },
      "reviewed-plan",
    );
    // The restored host draft is authoritative.
    expect(composerDraftForSession("s1")).toBe("rewrite the greeting");
  });

  it("edit restages path-file chips from restored_content_parts", async () => {
    const fence =
      '```attachment filename="main.go" mime="text/plain" truncated="false"\n' +
      "[User attached file: src/main.go]\n```";
    const client = fakeClient("review this", {
      restored_content_parts: [
        {
          content: "review this",
          origin: "user",
          authority: "user",
          trust_tier: "trusted",
        },
        {
          content: fence,
          origin: "retrieval",
          authority: "none",
          trust_tier: "untrusted",
          source: "src/main.go",
          path: "src/main.go",
          root_id: "root-a",
          media_type: "text/plain",
          reference_kind: "path_file",
        },
      ],
    });
    await runRewind(
      {
        client,
        appStore,
        projectId: "p1",
        sessionId: "s1",
        projectDir: "/proj",
        projects: [
          {
            id: "p1",
            name: "Proj",
            roots: [{ id: "root-a", path: "/proj", is_primary: true }],
          } as never,
        ],
      },
      { operationId: "operation-1", action: "edit", messageId: "u1", text: "stale" },
      "reviewed-plan",
    );
    expect(composerDraftForSession("s1")).toBe("review this");
    const pending = pendingAttachmentsForSession("s1");
    expect(pending).toHaveLength(1);
    expect(pending[0]).toMatchObject({
      kind: "path-file",
      path: "src/main.go",
      rootId: "root-a",
      projectId: "p1",
    });
  });

  it("rewind puts the restored ask in the composer draft", async () => {
    const client = fakeClient("the original ask");
    await runRewind(
      {
        client,
        appStore,
        projectId: "p1",
        sessionId: "s1",
        projectDir: "/p",
        projects: [],
      },
      { operationId: "operation-1", action: "rewind", messageId: "u1", text: "stale local copy" },
      "reviewed-plan",
    );
    expect(composerDraftForSession("s1")).toBe("the original ask");
  });

  it("always asks the host for before_turn and refetches the transcript", async () => {
    const client = fakeClient();
    await runRewind(
      {
        client,
        appStore,
        projectId: "p1",
        sessionId: "s1",
        projectDir: "/p",
        projects: [],
      },
      { operationId: "operation-1", action: "rewind", messageId: "u7", text: "x" },
      "reviewed-plan",
    );
    expect(client.rewindSession).toHaveBeenCalledWith("s1", {
      operation_id: expect.any(String),
      message_id: "u7",
      mode: "before_turn",
      plan_digest: "reviewed-plan",
    });
    // Rewind refetches the truncated transcript.
    expect(client.listSessionMessages).toHaveBeenCalledWith("s1");
  });

  it("leaves idle enforcement to the host", async () => {
    const client = {
      rewindSession: vi.fn(async () => {
        throw new Error("session_not_idle");
      }),
      listSessionMessages: vi.fn(async () => ({ messages: [], watermark: 0 })),
    };
    await expect(
      runRewind(
        {
          client: client as typeof client & LycaonClient,
          appStore,
          projectId: "p1",
          sessionId: "s1",
          projectDir: "/p",
          projects: [],
        },
        { operationId: "operation-1", action: "rewind", messageId: "u1", text: "the ask" },
      "reviewed-plan",
      ),
    ).rejects.toThrow("session_not_idle");
    expect(client.rewindSession).toHaveBeenCalledTimes(1);
    expect(client.listSessionMessages).not.toHaveBeenCalled();
  });

  it("leaves the draft untouched when the host refuses", async () => {
    const refusing = {
      rewindSession: vi.fn(async () => {
        throw new Error("session_not_idle");
      }),
      listSessionMessages: vi.fn(),
    };
    const client = refusing as typeof refusing & LycaonClient;
    await expect(
      runRewind(
        {
          client,
          appStore,
          projectId: "p1",
          sessionId: "s1",
          projectDir: "/p",
          projects: [],
        },
        { operationId: "operation-1", action: "edit", messageId: "u1", text: "the ask" },
      "reviewed-plan",
      ),
    ).rejects.toThrow("session_not_idle");
    expect(composerDraftForSession("s1")).toBe("");
    expect(refusing.listSessionMessages).not.toHaveBeenCalled();
  });
});

describe("restored attachment identity", () => {
  it("restages uploaded text and document blob IDs", () => {
    const pending = pendingAttachmentsFromRestore({
      projectId: "p1",
      sessionId: "destination",
      project: undefined,
      contentParts: [
        {
          content: '```attachment filename="notes.txt" mime="text/plain" truncated="false"\nhello\n```',
          origin: "attachment",
          authority: "none",
          trust_tier: "untrusted",
          source: "notes.txt",
          media_type: "text/plain",
          blob_id: "a".repeat(64),
          size_bytes: 5,
        },
      ],
    });
    expect(pending).toMatchObject([
      { kind: "text", name: "notes.txt", blobId: "a".repeat(64), byteLength: 5 },
    ]);
  });

  it("preserves a search hit's source session", () => {
    const pending = pendingAttachmentsFromRestore({
      projectId: "p1",
      sessionId: "destination",
      project: undefined,
      contentParts: [
        {
          content: "search result",
          origin: "retrieval",
          authority: "none",
          trust_tier: "untrusted",
          source: "msg-9",
          reference_kind: "search_hit",
          source_ref: "msg-9",
          hit_kind: "message",
          source_session_id: "source-session",
        },
      ],
    });
    expect(pending).toMatchObject([
      { kind: "search-hit", sessionId: "source-session", sourceRef: "msg-9" },
    ]);
  });

  it("skips incomplete attachment identities", () => {
    const pending = pendingAttachmentsFromRestore({
      projectId: "p1",
      sessionId: "destination",
      project: undefined,
      contentParts: [
        {
          content: "missing size",
          origin: "attachment",
          authority: "none",
          trust_tier: "untrusted",
          source: "notes.txt",
          media_type: "text/plain",
          blob_id: "a".repeat(64),
        },
        {
          content: "missing source session",
          origin: "retrieval",
          authority: "none",
          trust_tier: "untrusted",
          source: "msg-9",
          reference_kind: "search_hit",
          source_ref: "msg-9",
          hit_kind: "message",
        },
        {
          content: "missing source ref",
          origin: "retrieval",
          authority: "none",
          trust_tier: "untrusted",
          source: "msg-10",
          reference_kind: "search_hit",
          hit_kind: "message",
          source_session_id: "source-session",
        },
      ],
    });
    expect(pending).toEqual([]);
  });
});
