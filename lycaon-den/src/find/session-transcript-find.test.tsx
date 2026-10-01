import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { ChatSpanBlocks } from "../components/transcript/transcript-viewport-test-harness.tsx";
import {
  findController,
  findNext,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { FindBar } from "./FindBar.tsx";

afterEach(() => {
  resetFindControllerForTests();

  document.body.replaceChildren();
});

describe("session transcript findable adapter", () => {
  it("finds a unique needle across the resident chat transcript with next wrap", async () => {
    const messages = [
      {
        id: "u1",
        role: "user" as const, origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "please find UNIQUE_NEEDLE_ALPHA here",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "a1",
        role: "assistant" as const, origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "also UNIQUE_NEEDLE_ALPHA again",
        created_at: "2026-01-01T00:00:01Z",
      },
    ];

    const blocks = [
      {
        kind: "run" as const,
        runId: "ambient",
        ambientSpan: true,
        items: [
          { kind: "user" as const, key: "u1", text: messages[0]!.content },
          { kind: "assistant" as const, key: "a1", text: messages[1]!.content },
        ],
      },
    ];

    render(() => (
      <>
        <FindBar />
        <ChatSpanBlocks
          blocks={blocks}
          sessionId="s-find"
          messages={messages}
          workers={[]}
          visibleTurnActive={false}
        />
      </>
    ));

    expect(await screen.findByTestId("message-stream")).toBeTruthy();
    openFind();
    setFindQuery("UNIQUE_NEEDLE_ALPHA");
    expect(findController.matches().length).toBeGreaterThanOrEqual(2);
    expect(findController.activeIndex()).toBe(0);
    expect(
      document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length,
    ).toBeGreaterThanOrEqual(2);

    const before = findController.activeIndex();
    findNext();
    expect(findController.activeIndex()).not.toBe(before);

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
  });
});
