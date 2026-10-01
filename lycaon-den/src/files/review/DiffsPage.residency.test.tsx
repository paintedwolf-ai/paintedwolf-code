import { cleanup, render, screen, waitFor, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import type { SourceWalkResponse } from "../../api/types.ts";
import { invokeCommand } from "../../shortcuts/dispatcher.ts";
import { stubFilesClient, type ComparisonLoader } from "../../test/source-client-fixture.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { walkEffectFixture } from "../walk/walk-fixtures.ts";
import { resetScopeResolutionForTests } from "../tree/scope-resolution.ts";
import { DiffsPage } from "./DiffsPage.tsx";
import type { DiffsAddress } from "./diffs-address.ts";
import { resetGitDiffsForTests } from "./git-diffs.ts";
import { resetFilesStagePaneForTests } from "./review-pane.ts";
import { resetTurnDiffsForTests } from "./turn-diffs.ts";

const TURN: DiffsAddress = { kind: "turn", sessionId: "s1", turn: 4, messageId: "s1-user-4" };

function file(path: string): SourceWalkResponse["files"][number] {
  return {
    file_id: `file-${path}`, root_id: "r1", path,
    changed_since_presented: false, unpresented_agent_effects: 0,
    tip: { state: "content", sha256: "tip" }, head_match: "unknown",
    effects: [{ ...walkEffectFixture(`${path}-1`, 4, 1), file_id: `file-${path}`, path }],
  };
}

const client = () => stubFilesClient({
  listProjectSourceWalk: async (_projectId: string, opts?: { baseline?: string }) => ({
    baseline: opts?.baseline ?? "presentation",
    files: [file("a.go"), file("b.go")], turns: [], commands: [], git_changes: [],
    commit_available: true,
  }),
  readComparison: (async () => ({
    in_range: true, location_changed: false,
    before: { state: "content", size_bytes: 4, availability: "available", content: "old\n" },
    after: { state: "content", size_bytes: 4, availability: "available", content: "new\n" },
  })) as ComparisonLoader,
});

afterEach(() => {
  cleanup();
  resetTurnDiffsForTests();
  resetGitDiffsForTests();
  resetScopeResolutionForTests();
  resetFilesStagePaneForTests();
});

/** Two mounted diffs pages; `shown` names the one on display, the other prepares behind it. */
async function mountPair(order: readonly ["a" | "b", "a" | "b"], shown: () => "a" | "b") {
  const presence = (id: "a" | "b"): ResidentPresence => (shown() === id ? "active" : "pending");
  const page = (id: "a" | "b") => (
    <ResidentPresenceProvider presence={presence(id)}>
      <div data-testid={`page-${id}`}>
        <DiffsPage projectId="p1" client={client()} address={TURN} onWalkTurn={() => {}} />
      </div>
    </ResidentPresenceProvider>
  );
  render(() => <>{page(order[0])}{page(order[1])}</>);
  const fold = async (id: "a" | "b") => within(screen.getByTestId(`page-${id}`)).findByTestId("diffs-page-fold");
  return { a: await fold("a"), b: await fold("b") };
}

const RULE = "A retained diffs page answered a page command. Only the page on display may register " +
  "files.diff* handlers; gate their registration on useResidentPresence() === \"active\".";

describe("diffs page command routing", () => {
  for (const order of [["a", "b"], ["b", "a"]] as const) {
    it(`routes page commands to the displayed page when ${order[0]} mounts first`, async () => {
      const { a, b } = await mountPair(order, () => "a");
      const before = b.textContent;

      invokeCommand("files.diffToggleCollapse");

      await waitFor(() => expect(a.textContent, RULE).toBe("Expand all"));
      expect(b.textContent, RULE).toBe(before);
    });
  }

  it("moves page commands with the display", async () => {
    const [shown, setShown] = createSignal<"a" | "b">("b");
    const { a, b } = await mountPair(["a", "b"], shown);
    setShown("a");
    const before = b.textContent;

    invokeCommand("files.diffToggleCollapse");

    await waitFor(() => expect(a.textContent, RULE).toBe("Expand all"));
    expect(b.textContent, RULE).toBe(before);
  });
});
