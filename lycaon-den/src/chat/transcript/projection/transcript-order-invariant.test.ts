import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Message, WorkflowRun } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { compareTranscriptOrdSortKeys, sortTranscriptItemsByOrd, transcriptItemOrdSortKey, type TranscriptOrdSortKey } from "./transcript-item-order.ts";
import { type TranscriptItem } from "./transcript-item-model.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";
import { loadOrderingFixture } from "../testdata/ordering/assert-canonical-order.ts";

const fixture = loadOrderingFixture();
const fixtureMessages: readonly Message[] = fixture.messages;

/** Every adjacent pair uses the production (ord, sub) ordering. */
function expectOrderedByOrd(
  items: readonly TranscriptItem[],
  messages: readonly Message[],
): void {
  expect(items.length).toBeGreaterThan(0);
  const keys: TranscriptOrdSortKey[] = items.map((item) =>
    transcriptItemOrdSortKey(item, messages),
  );
  for (let i = 1; i < keys.length; i++) {
    const cmp = compareTranscriptOrdSortKeys(keys[i - 1]!, keys[i]!);
    expect(
      cmp,
      `out of ord order at index ${i}: ` +
        `${items[i - 1]!.kind}:${items[i - 1]!.key} ` +
        `(ord=${keys[i - 1]!.ord},sub=${keys[i - 1]!.sub}) > ` +
        `${items[i]!.kind}:${items[i]!.key} ` +
        `(ord=${keys[i]!.ord},sub=${keys[i]!.sub})`,
    ).toBeLessThanOrEqual(0);
  }
}

function itemKeys(items: readonly TranscriptItem[]): string[] {
  return items.map((item) => item.key);
}

/** Deterministic arrival permutations exercise the same ordering. */
function permutations<T>(base: readonly T[]): T[][] {
  const arr = [...base];
  const reversed = [...arr].reverse();
  const swapPairs = [...arr];
  for (let i = 0; i + 1 < swapPairs.length; i += 2) {
    [swapPairs[i], swapPairs[i + 1]] = [swapPairs[i + 1]!, swapPairs[i]!];
  }
  const seeded = [...arr];
  let seed = 2654435761;
  for (let i = seeded.length - 1; i > 0; i--) {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    const j = seed % (i + 1);
    [seeded[i], seeded[j]] = [seeded[j]!, seeded[i]!];
  }
  return [arr, reversed, swapPairs, seeded];
}

const catalogRun: WorkflowRun = {
  id: fixture.workflow_run_id,
  session_id: fixture.session_id,
  workflow_id: "release",
  workflow_version: "1.0.0",
	revision: 1,
  status: "running",
  current_phase: "plan",
  created_at: "t",
  updated_at: "t",
};

describe("transcript ord-order invariant", () => {
  it("transcript projector is non-decreasing on production (ord, sub)", () => {
    const items = createTranscriptDisplayProjector()(fixtureMessages, undefined, { verboseMode: true })[0]!;
    expectOrderedByOrd(items, fixtureMessages);
  });

  it("sortTranscriptItemsByOrd is idempotent and arrival-invariant", () => {
    const built = messagesToTranscriptItems(fixtureMessages, {
      verboseMode: true,
    });
    const baseline = sortTranscriptItemsByOrd(built, fixtureMessages);
    expectOrderedByOrd(baseline, fixtureMessages);
    expect(itemKeys(sortTranscriptItemsByOrd(baseline, fixtureMessages))).toEqual(
      itemKeys(baseline),
    );
    for (const perm of permutations(built)) {
      expect(itemKeys(sortTranscriptItemsByOrd(perm, fixtureMessages))).toEqual(
        itemKeys(baseline),
      );
    }
  });

  it("message arrival order still yields non-decreasing (ord, sub)", () => {
    const baseline = itemKeys(
      createTranscriptDisplayProjector()(fixtureMessages, undefined, { verboseMode: true })[0]!,
    );
    for (const perm of permutations(fixtureMessages)) {
      const items = createTranscriptDisplayProjector()(perm, undefined, { verboseMode: true })[0]!;
      expectOrderedByOrd(items, perm);
      expect(itemKeys(items)).toEqual(baseline);
    }
  });

  it("span-prebuilt display path stays non-decreasing on (ord, sub)", () => {
    const stamped = fixtureMessages.map((m) =>
      m.workflow_run_id
        ? m
        : { ...m, workflow_run_id: fixture.workflow_run_id },
    );
    const blocks = buildChatTranscriptBlocks(stamped, [catalogRun], catalogRun, {
      verboseMode: true,
    });
    expect(blocks.length).toBeGreaterThan(0);
    for (const block of blocks) {
      const items = createTranscriptDisplayProjector()(stamped, [block.items], { verboseMode: true })[0]!;
      expectOrderedByOrd(items, stamped);
    }
  });

  it("patching any message (new seq, same ord) never moves or drops a row", () => {
    const baseline = createTranscriptDisplayProjector()(fixtureMessages, undefined, {
      verboseMode: true,
    })[0]!;
    expectOrderedByOrd(baseline, fixtureMessages);
    for (const msg of fixtureMessages) {
      const patched = fixtureMessages.map((m) =>
        m.id === msg.id ? { ...m, seq: (m.seq ?? 0) + 1000 } : m,
      );
      const items = createTranscriptDisplayProjector()(patched, undefined, { verboseMode: true })[0]!;
      expectOrderedByOrd(items, patched);
      expect(itemKeys(items)).toEqual(itemKeys(baseline));
    }
  });

  it("grep guard: no Date.parse in the transcript ordering path", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const src = ["transcript-items.ts", "transcript-item-order.ts", "transcript-display-projection.ts"]
      .map((file) => readFileSync(join(here, file), "utf8")).join("\n");
    expect(src).not.toMatch(/Date\.parse/);
  });

  it("grep guard: order/identity stabilizers are absent from the source", () => {
    const here = dirname(fileURLToPath(import.meta.url));
    const itemsSrc = ["transcript-items.ts", "transcript-item-order.ts", "transcript-display-projection.ts"]
      .map((file) => readFileSync(join(here, file), "utf8")).join("\n");
    const spansSrc = readFileSync(join(here, "../../workflow/workflow-spans.ts"), "utf8");
    expect(itemsSrc).not.toMatch(/stabilizeTranscriptItems/);
    expect(spansSrc).not.toMatch(/stabilizeChatSpanBlocks/);
  });
});
