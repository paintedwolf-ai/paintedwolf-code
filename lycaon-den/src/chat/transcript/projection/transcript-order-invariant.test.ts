import { describe, expect, it } from "vitest";
import type { Message, WorkflowRun } from "../../../api/types.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { compareTranscriptOrdSortKeys, sortTranscriptItemsByOrd, transcriptItemOrdSortKey, type TranscriptOrdSortKey } from "./transcript-item-order.ts";
import type { DisplayTranscriptItem, RawTranscriptItem, TranscriptBuildOptions } from "./transcript-item-model.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";
import { loadOrderingFixture } from "../testdata/ordering/assert-canonical-order.ts";

const fixture = loadOrderingFixture();
const fixtureMessages: readonly Message[] = fixture.messages;

const verbose: TranscriptBuildOptions = { verboseMode: true };

/** The placed rows a display row draws; a diff card trails its group and has no place of its own. */
function placedRows(items: readonly (RawTranscriptItem | DisplayTranscriptItem)[]): RawTranscriptItem[] {
  return items.flatMap((item): RawTranscriptItem[] => {
    if (item.kind === "activity_span") {
      return item.entries.map((entry) =>
        entry.kind === "tool" ? { ...entry, key: entry.part.id } : entry,
      );
    }
    if (item.kind === "worker_group") {
      return item.parts.map((part) => ({ kind: "tool", key: part.id, part }));
    }
    switch (item.kind) {
      case "file_edit":
      case "pending_user":
      case "time_marker":
      case "unread_marker":
      case "turn_tail":
        return [];
      default:
        return [item];
    }
  });
}

/** Every adjacent pair of placed rows is in production order. */
function expectOrderedByOrd(
  items: readonly (RawTranscriptItem | DisplayTranscriptItem)[],
  messages: readonly Message[],
): void {
  const rows = placedRows(items);
  expect(rows.length).toBeGreaterThan(0);
  const keys: TranscriptOrdSortKey[] = rows.map((item) =>
    transcriptItemOrdSortKey(item, messages, verbose),
  );
  const show = (at: number) =>
    `${rows[at]!.kind}:${rows[at]!.key} (ord=${keys[at]!.ord},tier=${keys[at]!.tier},index=${keys[at]!.index})`;
  for (let i = 1; i < keys.length; i++) {
    expect(
      compareTranscriptOrdSortKeys(keys[i - 1]!, keys[i]!),
      `out of order at index ${i}: ${show(i - 1)} > ${show(i)}`,
    ).toBeLessThanOrEqual(0);
  }
}

function itemKeys(items: readonly { key: string }[]): string[] {
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
  it("transcript projector draws placed rows in production order", () => {
    const items = createTranscriptDisplayProjector()(fixtureMessages, undefined, { verboseMode: true })[0]!;
    expectOrderedByOrd(items, fixtureMessages);
  });

  it("sortTranscriptItemsByOrd is idempotent and arrival-invariant", () => {
    const built = messagesToTranscriptItems(fixtureMessages, {
      verboseMode: true,
    });
    const baseline = sortTranscriptItemsByOrd(built, fixtureMessages, verbose);
    expectOrderedByOrd(baseline, fixtureMessages);
    expect(itemKeys(sortTranscriptItemsByOrd(baseline, fixtureMessages, verbose))).toEqual(
      itemKeys(baseline),
    );
    for (const perm of permutations(built)) {
      expect(itemKeys(sortTranscriptItemsByOrd(perm, fixtureMessages, verbose))).toEqual(
        itemKeys(baseline),
      );
    }
  });

  it("message arrival order still yields production order", () => {
    const baseline = itemKeys(
      createTranscriptDisplayProjector()(fixtureMessages, undefined, { verboseMode: true })[0]!,
    );
    for (const perm of permutations(fixtureMessages)) {
      const items = createTranscriptDisplayProjector()(perm, undefined, { verboseMode: true })[0]!;
      expectOrderedByOrd(items, perm);
      expect(itemKeys(items)).toEqual(baseline);
    }
  });

  it("span-prebuilt display path stays in production order", () => {
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
});
