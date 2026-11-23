import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { ChatSpanBlocks } from "../components/transcript/transcript-viewport-test-harness.tsx";
import { toolApprovalFixture } from "../chat/checkpoint/approval-test-fixtures.ts";
import { foldIntervalFileEdits } from "../chat/file-edit/file-edit-fold.ts";
import { fileEditPreviewFixture } from "../test/file-edit-fixture.ts";
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

  it("discovers matches inside closed Approvals and Files edited sections of an activity group and cleans up temporary disclosures", async () => {
    const groupKey = "activity-group:a-find:call-1";
    const folds = foldIntervalFileEdits(groupKey, [
      {
        key: "step-find-1",
        partId: "part-find-1",
        toolCallId: "call-1",
        assistantMessageId: "a-find",
        messageId: "r-find-1",
        path: "src/needle_target_file.ts",
        preview: fileEditPreviewFixture({
          path: "src/needle_target_file.ts",
          before: "const oldVal = 1;\n",
          after: "const STACK_FIND_OMEGA = 2;\n",
        }),
      },
    ]);
    const blocks = [
      {
        kind: "run" as const,
        runId: "ambient",
        ambientSpan: true,
        items: [
          {
            kind: "activity_group" as const,
            key: groupKey,
            sectionOrder: ["approvals" as const, "files" as const],
            approvals: [
              {
                kind: "checkpoint" as const,
                key: "cp-find-1",
                parentMessageId: "a-find",
                toolCallId: "call-1",
                meta: {
                  id: "cp-find-1",
                  kind: "tool_approval" as const,
                  state: "approved" as const,
                  title: "Allow STACK_FIND_OMEGA primary gate",
                  tool_approval: toolApprovalFixture({
                    command: "run STACK_FIND_OMEGA",
                    title: "Allow STACK_FIND_OMEGA primary gate",
                  }),
                },
              },
              {
                kind: "checkpoint" as const,
                key: "cp-find-2",
                parentMessageId: "a-find",
                toolCallId: "call-2",
                meta: {
                  id: "cp-find-2",
                  kind: "tool_approval" as const,
                  state: "denied" as const,
                  title: "Allow secondary check",
                  tool_approval: toolApprovalFixture({
                    command: "check secondary",
                    title: "Allow secondary check",
                  }),
                },
              },
            ],
            folds,
            headline: "activity",
            entries: [],
            anchorMessageId: "a-find",
          },
        ],
      },
    ];

    const { container } = render(() => (
      <>
        <FindBar />
        <ChatSpanBlocks
          blocks={blocks}
          sessionId="s-find-stack"
          messages={[]}
          workers={[]}
          visibleTurnActive={false}
        />
      </>
    ));

    expect(await screen.findByTestId("message-stream")).toBeTruthy();
    const approvalsToggle = container.querySelector<HTMLButtonElement>('[data-testid="activity-group-approvals-toggle"]');
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");

    openFind();
    setFindQuery("STACK_FIND_OMEGA");
    expect(findController.matches().length).toBeGreaterThanOrEqual(2);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("true");

    findNext();
    const diffToggle = container.querySelector<HTMLButtonElement>('[data-testid="diff-group"] button.den-transcript-disclosure-toggle');
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("true");

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`)).toHaveLength(0);
    expect(approvalsToggle?.getAttribute("aria-expanded")).toBe("false");
    expect(diffToggle?.getAttribute("aria-expanded")).toBe("false");
  });
});
