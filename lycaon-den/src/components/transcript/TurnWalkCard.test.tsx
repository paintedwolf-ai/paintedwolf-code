import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";
import { TurnWalkCard } from "./TurnWalkCard.tsx";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import { reviewTurnsByAssistant } from "../../chat/transcript/presentation/review-turns.ts";
import type { DisplayTranscriptItem } from "../../chat/transcript/projection/transcript-item-model.ts";
import { WalkButton } from "../../files/walk/WalkButton.tsx";
import { refreshWalk, resetWalkForTests, walkState } from "../../files/walk/walk-store.ts";
import { refreshWalkPreviews, resetWalkPreviewsForTests } from "../../files/walk/walk-preview.ts";
import { walkClientFixture, walkEffectFixture, walkResponseFixture } from "../../files/walk/walk-fixtures.ts";

const reviewTurnForAssistant = (
  assistantKey: string,
  items: readonly DisplayTranscriptItem[],
  messages: readonly Message[],
  visibleTurnActive: boolean,
): string | null =>
  reviewTurnsByAssistant(items, new Map(messages.map((message) => [message.id, message])), visibleTurnActive)
    .get(assistantKey) ?? null;

afterEach(() => {
  cleanup(); resetWalkForTests(); resetWalkPreviewsForTests(); resetOpenFilesSurfaceForTests();
});

describe("Turn Walk cards in the live chat timeline", () => {
  it("opens a commit-only turn at its Git group without offering working-file diffs", async () => {
    const response = {...walkResponseFixture([]),
      git_changes: [{id: "commit", root_id: "r1", kind: "commit" as const, ordinal: 10, observed_at: "2026-09-18T04:20:00Z",
        session_id: "s1", turn: 2, tool_call_id: "commit-call", tool_name: "git_commit"}],
      turns: [{session_id: "s1", turn: 2, message_id: "s1-user-2", prompt: "Check in logical groups", observed_at: "2026-09-18T04:15:00Z"}],
    };
    const client = walkClientFixture(() => response);
    vi.spyOn(client, "getProjectSourceWalkSummary").mockResolvedValue([{message_id: "s1-user-2", turn: 2, steps: 1, items: 0}]);
    render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />);
    fireEvent.click(await screen.findByRole("button", {name: "Walk from here"}));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    expect(screen.queryByTestId("turn-diffs-action")).toBeNull();
    expect(walkState("p1").walk.steps[walkState("p1").at]).toMatchObject({kind: "git", key: "git:commit", toolCallId: "commit-call"});
    expect(walkState("p1").walk.chapters[0]).toMatchObject({messageId: "s1-user-2", turn: 2});
  });

  it("keeps the mounted changes card when the next prompt begins preparing", async () => {
    const client = walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 2, 1)]));
    const [active, setActive] = createSignal(false);
    const [messages, setMessages] = createSignal<Message[]>([
      { id: "s1-user-2", role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "Change a file", created_at: "2026-09-17T12:00:00Z", ord: 1 },
      { id: "answer", role: "assistant", origin: "model", authority: "none", trust_tier: "trusted", kind: "completion_report", content: "Finished", created_at: "2026-09-17T12:01:00Z", ord: 2 },
    ]);
    render(() => <SessionTranscript layout="chat" projectId="p1" sessionId="s1"
      messages={messages()} visibleTurnActive={active()} checkpointClient={client} />);
    const card = await screen.findByTestId("turn-walk-card");
    setActive(true);
    expect(screen.getByTestId("turn-walk-card")).toBe(card);
    setMessages((previous) => [...previous, {
      ...previous[0]!, id: "s1-user-3", content: "Next request", created_at: "2026-09-17T12:02:00Z", ord: 3,
    }]);
    expect(screen.getByTestId("turn-walk-card")).toBe(card);
  });

  it("opens the clicked chapter with the real toolbar mounted and keeps it through new turns", async () => {
    let effects = [walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2)];
    const client = walkClientFixture(() => walkResponseFixture(effects));
    const [turn, setTurn] = createSignal(6);
    const store = { get state() { return { currentSession: { id: "s1", title: "Fix the editor", current_turn: turn() } }; } } as AppStore;
    const sink = vi.fn(); registerOpenFilesSurfaceSink(sink);
    render(() => <>
      <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />
      <WalkButton projectId="p1" client={client} appStore={store} />
    </>);
    fireEvent.click(await screen.findByRole("button", { name: "Walk from here" }));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    expect(walkState("p1").sessionId).toBe("s1");
    expect(walkState("p1").walk.steps[walkState("p1").at]?.key).toBe("a");
    expect(walkState("p1").walk.chapters).toHaveLength(2);
    expect(sink).toHaveBeenCalledWith({ kind: "stage", projectId: "p1" });
    const comparison = walkState("p1").comparison;
    setTurn(7);
    effects = [...effects, walkEffectFixture("c", 7, 3)];
    await refreshWalk("p1");
    expect(walkState("p1").walk.chapters).toHaveLength(3);
    expect(walkState("p1").walk.steps[walkState("p1").at]?.key).toBe("a");
    expect(walkState("p1").comparison).toBe(comparison);
  });

  it("opens the turn's diffs beside Walk, addressed by the turn the host named", async () => {
    const client = walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 2, 1), walkEffectFixture("b", 2, 2)]));
    const sink = vi.fn(); registerOpenFilesSurfaceSink(sink);
    render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />);

    fireEvent.click(await screen.findByRole("button", { name: "All diffs" }));

    expect(sink).toHaveBeenCalledWith({
      kind: "diffs", projectId: "p1",
      address: { kind: "turn", sessionId: "s1", turn: 2, messageId: "s1-user-2" },
    });
    expect(sink).toHaveBeenCalledWith({ kind: "stage", projectId: "p1" });
    expect(screen.getByRole("button", { name: "Walk from here" })).toBeTruthy();
  });

  it("offers no diffs page for a turn that changed no file", async () => {
    const client = walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 2, 1)]));
    vi.spyOn(client, "getProjectSourceWalkSummary").mockResolvedValue([
      { message_id: "s1-user-2", turn: 2, steps: 1, items: 0 },
    ]);
    render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />);

    await screen.findByTestId("turn-walk-card");
    expect(screen.queryByTestId("turn-diffs-action")).toBeNull();
  });

  it("shares bounded previews between cards and updates their counts on source changes", async () => {
    let response = walkResponseFixture([walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2)]);
    const client = walkClientFixture(() => response);
    const read = vi.spyOn(client, "getProjectSourceWalkSummary");
    const history = vi.spyOn(client, "listProjectSourceWalk");
    render(() => <>
      <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />
      <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-5" client={client} />
    </>);
    await waitFor(() => expect(screen.getAllByTestId("turn-walk-card")).toHaveLength(2));
    expect(read).toHaveBeenCalledTimes(1);
    expect(history).not.toHaveBeenCalled();
    expect(read).toHaveBeenCalledWith("p1", "s1", ["s1-user-2", "s1-user-5"]);
    response = walkResponseFixture([...response.files[0]!.effects, walkEffectFixture("c", 5, 3)]);
    refreshWalkPreviews("p1", "s1");
    expect(await screen.findByText("2 steps across 1 item")).toBeTruthy();
    expect(read).toHaveBeenCalledTimes(2);
    expect(history).not.toHaveBeenCalled();
  });

  it("uses the opening message identity even when earlier messages are not loaded", () => {
    const messages = [
      { id: "s1-user-12", role: "user", content: "twelfth" },
      { id: "answer", role: "assistant", content: "done" },
    ] as Message[];
    expect(reviewTurnForAssistant("answer", [
      { kind: "user", key: "s1-user-12", text: "twelfth" },
      { kind: "assistant", key: "answer", text: "done" },
    ], messages, false)).toBe("s1-user-12");
  });

  it("renders a remounted card at creation from the host's last answer", async () => {
    const client = walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 2, 1)]));
    const first = render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />);
    await screen.findByTestId("turn-walk-card");
    first.unmount();

    render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-2" client={client} />);

    expect(screen.getByTestId("turn-walk-card")).toBeTruthy();
  });

  it("never substitutes another chapter when the requested turn has no retained steps", async () => {
    const client = walkClientFixture(() => walkResponseFixture([walkEffectFixture("a", 2, 1)]));
    render(() => <TurnWalkCard projectId="p1" sessionId="s1" messageId="s1-user-9" client={client} />);
    await new Promise((resolve) => setTimeout(resolve, 450));
    expect(screen.queryByTestId("turn-walk-card")).toBeNull();
  });
});

describe("reviewTurnForAssistant", () => {
  const messages = [
    { id: "u1", role: "user", content: "one", created_at: "2026-08-22T00:00:00Z" },
    { id: "a1", role: "assistant", content: "first", created_at: "2026-08-22T00:00:01Z" },
    { id: "a2", role: "assistant", content: "final", created_at: "2026-08-22T00:00:02Z" },
    { id: "u2", role: "user", content: "two", created_at: "2026-08-22T00:00:03Z" },
    { id: "a3", role: "assistant", content: "current", created_at: "2026-08-22T00:00:04Z" },
  ] as unknown as Message[];
  const items = [
    { kind: "user" as const, key: "u1", text: "one" },
    { kind: "assistant" as const, key: "a1", text: "first" },
    { kind: "assistant" as const, key: "a2", text: "final" },
    { kind: "user" as const, key: "u2", text: "two" },
    { kind: "assistant" as const, key: "a3", text: "current" },
  ];

  it("chooses only the terminal assistant row for each structural turn", () => {
    expect(reviewTurnForAssistant("a1", items, messages, false)).toBeNull();
    expect(reviewTurnForAssistant("a2", items, messages, false)).toBe("u1");
    expect(reviewTurnForAssistant("a3", items, messages, false)).toBe("u2");
  });

  it("keeps a committed report reviewable through activity, pending send, and the next turn", () => {
    const reported = messages.map((message) => message.id === "a3"
      ? { ...message, kind: "completion_report" as const } : message);
    const pending = {
      kind: "pending_user" as const, key: "pending", text: "next",
      pending: { kind: "prompt" as const, operationId: "pending", text: "next", state: "sending" as const, createdAt: 1 },
    };
    expect(reviewTurnForAssistant("a3", items, reported, false)).toBe("u2");
    expect(reviewTurnForAssistant("a3", items, reported, true)).toBe("u2");
    expect(reviewTurnForAssistant("a3", [...items, pending], reported, true)).toBe("u2");
    expect(reviewTurnForAssistant("a3", [
      ...items, { kind: "user", key: "u3", text: "next" },
    ], [...reported, { ...messages[0]!, id: "u3" }], true)).toBe("u2");
    expect(reviewTurnForAssistant("a3", items, reported.map((message) => message.id === "a3"
      ? { ...message, status: "streaming" as const } : message), true)).toBeNull();
  });

  it("a pending send does not close the host's current turn", () => {
    expect(reviewTurnForAssistant("a3", [...items, {
      kind: "pending_user", key: "pending", text: "next",
      pending: { kind: "prompt", operationId: "pending", text: "next", state: "sending", createdAt: 1 },
    }], messages, true)).toBeNull();
  });

  it("holds the current card for the whole visible turn, including model gaps", () => {
    expect(
      reviewTurnForAssistant("a3", items, messages, true),
    ).toBeNull();
  });
});
