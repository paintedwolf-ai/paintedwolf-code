import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import type { JSX } from "solid-js";
import { stubFilesClient } from "../../test/source-client-fixture.ts";
import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { disclosureToggle } from "../../test/transcript-disclosure-fixture.ts";
import { foldFileEdits } from "../../chat/file-edit/file-edit-fold.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { resetFindRevealHostsForTests } from "../../find/find-reveal.ts";
import { resetFindControllerForTests } from "../../find/find-controller.ts";
import { invalidateApprovalGrantsCache } from "../../settings/security/approval-grants-cache.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { CheckpointDecisionChicklet } from "../checkpoint/CheckpointDecisionChicklet.tsx";
import { ProgressSnapshot } from "./ProgressSnapshot.tsx";
import { TranscriptDiffGroup } from "./TranscriptDiffGroup.tsx";

const client = stubFilesClient();
vi.mock("../../platform/connection/app-connection.ts", async (original) => ({
  ...(await original<typeof import("../../platform/connection/app-connection.ts")>()),
  getLycaonClient: () => client,
}));
vi.mock("../../platform/navigation/open-source.ts", async (original) => ({
  ...(await original<typeof import("../../platform/navigation/open-source.ts")>()),
  openSourceLocation: vi.fn(),
}));

afterEach(() => {
  cleanup();
  clearTranscriptEntryMemory();
  resetFindControllerForTests();
  resetFindRevealHostsForTests();
  invalidateApprovalGrantsCache();
  vi.clearAllMocks();
  document.body.replaceChildren();
});

type Card = { name: string; mount: () => HTMLElement };

function mountCard(view: () => JSX.Element, testId: string): HTMLElement {
  const { getByTestId } = render(view);
  return getByTestId(testId);
}

function diffGroup() {
  const folds = foldFileEdits([
    {
      key: "t1",
      snapshot: fileEditPreviewFixture({ path: "src/pipeline.py", before: "one\n", after: "two\n" }),
      tool: "edit",
    },
    {
      key: "t2",
      snapshot: fileEditPreviewFixture({ path: "src/cli.py", before: "a\n", after: "b\n" }),
      tool: "edit",
    },
  ]);
  return <TranscriptDiffGroup folds={folds} layout="chat" sessionId="s1" entryKey="group-1" projectId="project" />;
}

const CARDS: Card[] = [
  {
    name: "a progress update",
    mount: () =>
      mountCard(
        () => (
          <ProgressSnapshot
            kind="updated"
            sessionId="s1"
            entryKey="progress-1"
            changes={[
              { kind: "done", label: "Implement the `board` logic", state: "done" },
              { kind: "created", label: "Verify build", state: "pending" },
            ]}
          />
        ),
        "progress-strip-delta",
      ),
  },
  {
    name: "an approval decision",
    mount: () =>
      mountCard(
        () => (
          <CheckpointDecisionChicklet
            projectId="project"
            sessionId="s1"
            entryKey="checkpoint-1"
            meta={{
              checkpoint_id: "chk-1",
              kind: "tool_approval",
              status: "approved",
              tool: "command",
              subject: "cargo build --release",
              location: "local repo",
              grant_title: "Allow for this chat",
              grant_scope: "chat",
            }}
          />
        ),
        "checkpoint-decision-chicklet",
      ),
  },
  { name: "a diff group", mount: () => mountCard(diffGroup, "diff-group") },
  {
    name: "a file in a diff group",
    mount: () => {
      const group = mountCard(diffGroup, "diff-group");
      fireEvent.click(disclosureToggle(group));
      const row = group.querySelector<HTMLElement>('[data-testid="file-edit-diff"][data-path="src/cli.py"]');
      if (!row) throw new Error("diff row not found");
      return row;
    },
  },
];

function summaryOf(card: HTMLElement): HTMLElement {
  const summary = card.querySelector<HTMLElement>(":scope > [data-disclosure-summary]");
  if (!summary) throw new Error("card has no summary");
  return summary;
}

/** Every element of a closed summary except declared actions. */
function clickTargets(card: HTMLElement): HTMLElement[] {
  const summary = summaryOf(card);
  return [summary, ...summary.querySelectorAll<HTMLElement>("*")].filter(
    (element) => !element.closest("[data-disclosure-action]"),
  );
}

describe("a closed transcript card", () => {
  it.each(CARDS)("opens $name from anywhere in its summary", ({ mount }) => {
    const count = clickTargets(mount()).length;
    cleanup();
    expect(count).toBeGreaterThan(2);
    for (let index = 0; index < count; index += 1) {
      const card = mount();
      const target = clickTargets(card)[index]!;
      expect(summaryOf(card).getAttribute("data-disclosure-summary")).toBe("closed");
      fireEvent.click(target);
      expect(card.getAttribute("data-expanded"), target.outerHTML).toBe("true");
      cleanup();
    }
    expect(openSourceLocation).not.toHaveBeenCalled();
  });

  it("keeps an open file's path a link to the file", () => {
    const row = CARDS[3]!.mount();
    fireEvent.click(summaryOf(row));
    fireEvent.click(row.querySelector<HTMLElement>('[data-testid="source-path-link"]')!);
    expect(openSourceLocation).toHaveBeenCalledOnce();
  });
});
