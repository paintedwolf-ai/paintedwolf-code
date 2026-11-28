import { resetSessionTranscriptChatTest } from "./session-transcript-chat-test-harness.ts";

import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import type { FileEditSnapshot, Message } from "../../api/types.ts";

type CommandCall = { id: string; edits?: FileEditSnapshot[]; omitted?: number };

/** Rows are ordered by their place in the session. */
function at(seq: number) {
  return { created_at: `2026-01-01T00:00:${String(seq).padStart(2, "0")}Z`, seq, ord: seq };
}

/** One assistant turn of `command` calls and their settled result rows. */
function commandTurn(turn: string, seq: number, calls: CommandCall[]): Message[] {
  const assistant = {
    id: `assistant-${turn}`,
    role: "assistant", origin: "model", authority: "none", trust_tier: "trusted",
    content: "",
    tool_calls: calls.map((call) => ({ id: call.id, name: "command", args: { command: `run ${call.id}` } })),
    ...at(seq),
  } as Message;
  const results = calls.map((call, index) => ({
    id: `result-${call.id}`,
    role: "tool", origin: "tool", authority: "none", trust_tier: "untrusted",
    content: "{}",
    tool_result: {
      tool_call_id: call.id,
      tool: "command",
      assistant_message_id: assistant.id,
      tool_args: { command: `run ${call.id}` },
      content: "{}",
      file_edit_previews: (call.edits ?? []).map(fileEditPreviewFixture),
      file_edits_omitted: call.omitted,
    },
    ...at(seq + 1 + index),
  }) as Message);
  return [assistant, ...results];
}

function user(id: string, seq: number): Message {
  return {
    id, role: "user", origin: "user", authority: "user", trust_tier: "trusted",
    content: "next", ...at(seq),
  } as Message;
}

function groups(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>('[data-testid="diff-group"]')];
}

function groupText(group: HTMLElement, testId: string): string {
  return group.querySelector(`[data-testid="${testId}"]`)?.textContent ?? "";
}

describe("SessionTranscript diffs of command edits", () => {
  afterEach(resetSessionTranscriptChatTest);

  it("shows a command's diff in the quiet transcript", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-quiet" messages={commandTurn("1", 1, [
        { id: "c1", edits: [{ path: "a.go", before: "old\n", after: "new\n" }] },
        { id: "c2" },
      ])} />
    ));
    expect(groups(container)).toHaveLength(1);
    expect(groups(container)[0]!.getAttribute("data-files")).toBe("1");
  });

  it("groups every file one command edited under that command", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-multi" messages={commandTurn("1", 1, [
        { id: "c1", edits: [{ path: "a.go", before: "old\n", after: "new\n" }, { path: "b.go", after: "created\n" }] },
      ])} />
    ));
    const [group] = groups(container);
    expect(groups(container)).toHaveLength(1);
    expect(group!.getAttribute("data-files")).toBe("2");
    expect(groupText(group!, "diff-group-files")).toBe("2 files edited");
    expect(groupText(group!, "diff-group-writes")).toBe("2 writes");
    expect(groupText(group!, "diff-group-hint")).toBe("a.go · b.go");
  });

  it("composes later commands' writes to a file onto its first card", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-compose" messages={commandTurn("1", 1, [
        { id: "c1", edits: [{ path: "a.go", before: "v0\n", after: "v1\n" }] },
        { id: "c2", edits: [{ path: "a.go", before: "v1\n", after: "v2\n" }] },
      ])} />
    ));
    const [group] = groups(container);
    expect(groups(container)).toHaveLength(1);
    expect(group!.getAttribute("data-files")).toBe("1");
    expect(groupText(group!, "diff-group-writes")).toBe("2 writes");
  });

  it("keeps the same path in two roots as two files", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-roots" messages={commandTurn("1", 1, [
        { id: "c1", edits: [
          { root_id: "11111111-1111-4111-8111-111111111111", path: "README.md", before: "a\n", after: "b\n" },
          { root_id: "22222222-2222-4222-8222-222222222222", path: "README.md", before: "a\n", after: "b\n" },
        ] },
      ])} />
    ));
    expect(groups(container)[0]!.getAttribute("data-files")).toBe("2");
  });

  it("counts a removed file as a changed file", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-deleted" messages={commandTurn("1", 1, [
        { id: "c1", edits: [{ path: "old.go", before: "package old\n", after: "", deleted: true }] },
      ])} />
    ));
    expect(groups(container)[0]!.getAttribute("data-files")).toBe("1");
  });

  it("starts a new card for a file edited again after the person's next message", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-turns" messages={[
        ...commandTurn("1", 1, [{ id: "c1", edits: [{ path: "a.go", before: "v0\n", after: "v1\n" }] }]),
        user("u2", 10),
        ...commandTurn("2", 11, [{ id: "c2", edits: [{ path: "a.go", before: "v1\n", after: "v2\n" }] }]),
      ]} />
    ));
    expect(groups(container).map((group) => groupText(group, "diff-group-writes"))).toEqual(["1 write", "1 write"]);
  });

  it("adds a file to the card of a call whose result gains an edit", async () => {
    const before = commandTurn("1", 1, [{ id: "c1", edits: [{ path: "a.go", before: "old\n", after: "new\n" }] }]);
    const after = commandTurn("1", 1, [
      { id: "c1", edits: [{ path: "a.go", before: "old\n", after: "new\n" }, { path: "late.go", after: "later\n" }] },
    ]);
    const [messages, setMessages] = createSignal(before);
    const { container } = render(() => <SessionTranscript layout="chat" sessionId="s-late" messages={messages()} />);
    const [group] = groups(container);
    expect(group!.getAttribute("data-files")).toBe("1");

    setMessages(after);
    await Promise.resolve();
    await Promise.resolve();

    expect(groups(container)).toHaveLength(1);
    expect(groups(container)[0]).toBe(group);
    expect(group!.getAttribute("data-files")).toBe("2");
  });

  it("says how many changes a result left out, with or without a shown diff", () => {
    const { container } = render(() => (
      <SessionTranscript layout="chat" sessionId="s-omitted" messages={[
        ...commandTurn("1", 1, [{ id: "c1", edits: [{ path: "a.go", before: "old\n", after: "new\n" }], omitted: 5 }]),
        user("u2", 10),
        ...commandTurn("2", 11, [{ id: "c2", omitted: 1 }]),
      ]} />
    ));
    expect(groups(container).map((group) => groupText(group, "diff-group-files"))).toEqual([
      "1 file edited, 5 more changes in Review",
      "1 change in Review",
    ]);
  });

  it("folds edits interval-locally across agent_note boundaries and disambiguates duplicate basenames", () => {
    const note: Message = {
      id: "note-mid",
      role: "assistant",
      kind: "agent_note",
      origin: "model",
      authority: "none",
      trust_tier: "trusted",
      content: "First pass complete; updating second pass.",
      grounding: { traced: true, host_assembled: false, observed_path_count: 1, observed_paths_sample: ["a.go"] },
      ...at(5),
    };
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-intervals"
        rootRefs={[
          { id: "r1", path: "/work/api" },
          { id: "r2", path: "/work/web" },
        ]}
        messages={[
          ...commandTurn("1", 1, [
            { id: "c1", edits: [{ root_id: "r1", path: "README.md", before: "v0\n", after: "v1\n" }] },
            { id: "c2", edits: [{ root_id: "r1", path: "README.md", before: "v1\n", after: "v2\n" }, { root_id: "r2", path: "README.md", before: "w0\n", after: "w1\n" }] },
          ]),
          note,
          ...commandTurn("2", 6, [
            { id: "c3", edits: [{ root_id: "r1", path: "README.md", before: "v2\n", after: "v3\n" }] },
          ]),
        ]}
      />
    ));
    const rendered = groups(container);
    expect(rendered).toHaveLength(2);
    expect(groupText(rendered[0]!, "diff-group-writes")).toBe("3 writes");
    expect(groupText(rendered[0]!, "diff-group-hint")).toBe("api/README.md · web/README.md");
    expect(groupText(rendered[1]!, "diff-group-writes")).toBe("1 write");
  });

  it("bounds mounted file-edit-diff rows via VirtualCardList when 45 files are edited in one interval", () => {
    const manyEdits = Array.from({ length: 45 }, (_, idx) => ({
      path: `pkg/file_${idx}.go`,
      before: "old\n",
      after: `new ${idx}\n`,
    }));
    const { container } = render(() => (
      <SessionTranscript
        layout="chat"
        sessionId="s-45-files"
        messages={commandTurn("1", 1, [{ id: "c-bulk", edits: manyEdits }])}
      />
    ));
    const [group] = groups(container);
    expect(group!.getAttribute("data-files")).toBe("45");
    group!.querySelector<HTMLButtonElement>("button.den-transcript-disclosure-toggle")!.click();
    const mountedDiffs = group!.querySelectorAll('[data-testid="file-edit-diff"]');
    expect(mountedDiffs.length).toBeLessThan(45);
  });
});
