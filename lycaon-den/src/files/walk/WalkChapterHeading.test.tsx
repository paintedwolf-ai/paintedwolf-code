import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppStore } from "../../store/app-state-model.ts";
import { WalkButton } from "./WalkButton.tsx";
import { WalkChapterHeading } from "./WalkChapterHeading.tsx";
import { walkClientFixture, walkEffectFixture, walkResponseFixture } from "./walk-fixtures.ts";
import {
  enterWalk,
  refreshWalk,
  resetWalkForTests,
  setWalkAt,
  walkNewStepCount,
  walkNewTurnCount,
  walkState,
} from "./walk-store.ts";

afterEach(() => { cleanup(); resetWalkForTests(); });

describe("Live chapter navigation", () => {
  it("starts at the latest chapter with work when a new empty turn is running", async () => {
    const client = walkClientFixture(() => walkResponseFixture([
      walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2), walkEffectFixture("c", 5, 3),
    ]));
    const store = { state: { currentSession: { id: "s1", title: "Fix the editor", current_turn: 6 } } } as AppStore;
    render(() => <WalkButton projectId="p1" client={client} appStore={store} />);
    fireEvent.click(screen.getByTestId("walk-button"));
    await waitFor(() => expect(walkState("p1").status).toBe("ready"));
    expect(walkState("p1").at).toBe(1);
    expect(walkState("p1").walk.steps[1]?.key).toBe("b");
  });

  it("shows the current chapter without a picker and reveals its opening message", async () => {
    const response = walkResponseFixture([walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2)]);
    const client = walkClientFixture(() => response);
    await enterWalk("p1", client, "s1");
    const reveal = vi.fn();
    render(() => <WalkChapterHeading projectId="p1" onRevealInTranscript={reveal} />);

    expect(screen.getByTestId("walk-chapter-heading").textContent).toContain("Turn 5");
    expect(screen.queryByRole("button", { name: /jump to turn/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show in chat" }));
    expect(reveal).toHaveBeenCalledWith({
      sessionId: "s1",
      anchor: { chicklet: "message", anchorId: "s1-user-5" },
    });

    setWalkAt("p1", 0);
    await waitFor(() => expect(screen.getByTestId("walk-chapter-heading").textContent).toContain("Turn 2"));
    fireEvent.click(screen.getByRole("button", { name: "Show in chat" }));
    expect(reveal).toHaveBeenLastCalledWith({
      sessionId: "s1",
      anchor: { chicklet: "message", anchorId: "s1-user-2" },
    });
  });

  it("receives new chapters without moving the reader", async () => {
    let response = walkResponseFixture([walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2)]);
    const client = walkClientFixture(() => response);
    await enterWalk("p1", client, "s1");
    setWalkAt("p1", 0);
    await waitFor(() => expect(walkState("p1").at).toBe(0));
    const comparison = walkState("p1").comparison;
    render(() => <WalkChapterHeading projectId="p1" />);

    response = walkResponseFixture([...response.files[0]!.effects, walkEffectFixture("c", 6, 3)]);
    await refreshWalk("p1");

    expect(screen.getByTestId("walk-chapter-heading").textContent).toContain("Turn 2");
    expect(walkState("p1").at).toBe(0);
    expect(walkState("p1").comparison).toBe(comparison);
    expect(walkNewStepCount("p1")).toBe(1);
    expect(walkNewTurnCount("p1")).toBe(1);
  });

  it("honors navigation that finishes while a timeline refresh is in flight", async () => {
    const effects = [walkEffectFixture("a", 2, 1), walkEffectFixture("b", 2, 2)];
    const client = walkClientFixture(() => walkResponseFixture(effects));
    await enterWalk("p1", client, "s1");
    let resolve!: (response: ReturnType<typeof walkResponseFixture>) => void;
    client.listProjectSourceWalk = () => new Promise((done) => { resolve = done; });
    const pending = refreshWalk("p1");
    setWalkAt("p1", 1);
    await waitFor(() => expect(walkState("p1").at).toBe(1));
    const comparison = walkState("p1").comparison;
    resolve(walkResponseFixture([...effects, walkEffectFixture("c", 3, 3)]));
    await pending;
    expect(walkState("p1").at).toBe(1);
    expect(walkState("p1").comparison).toBe(comparison);
    expect(walkNewTurnCount("p1")).toBe(1);
  });

  it("cancels pending navigation when the reader reselects their current step", async () => {
    const effects = [walkEffectFixture("a", 1, 1), walkEffectFixture("b", 1, 2)];
    const client = walkClientFixture(() => walkResponseFixture(effects));
    const readComparison = client.readComparison.bind(client);
    let resolve: ((value: Awaited<ReturnType<typeof readComparison>>) => void) | undefined;
    client.readComparison = (...[projectId, target, options]: Parameters<typeof readComparison>) =>
      "effectId" in target && target.effectId === "b"
        ? new Promise((done) => { resolve = done; })
        : readComparison(projectId, target, options);
    await enterWalk("p1", client, "s1");
    const held = walkState("p1").comparison;
    await waitFor(() => expect(resolve).toBeTypeOf("function"));
    setWalkAt("p1", 1);
    setWalkAt("p1", 0);
    resolve!({ ...held!, after: { state: "content", size_bytes: 1, availability: "available", sha256: "b" } });
    await Promise.resolve();
    await Promise.resolve();
    expect(walkState("p1").at).toBe(0);
    expect(walkState("p1").targetAt).toBe(0);
    expect(walkState("p1").comparison).toBe(held);
  });

  it("uses a reconnected client for subsequent navigation without moving the reader", async () => {
    const response = walkResponseFixture([walkEffectFixture("a", 1, 1), walkEffectFixture("b", 1, 2)]);
    const original = walkClientFixture(() => response);
    const replacement = walkClientFixture(() => response);
    const readOriginal = vi.spyOn(original, "readComparison");
    const readReplacement = vi.spyOn(replacement, "readComparison");
    await enterWalk("p1", original, "s1");
    readOriginal.mockClear();
    await refreshWalk("p1", replacement);
    expect(walkState("p1").at).toBe(0);
    setWalkAt("p1", 1);
    await waitFor(() => expect(walkState("p1").at).toBe(1));
    expect(readOriginal).not.toHaveBeenCalled();
    expect(readReplacement.mock.calls.filter(([, target]) =>
      "effectId" in target && target.effectId === "b",
    )).toEqual([["p1", { effectId: "b" }, { sessionId: "s1" }]]);
  });
});
