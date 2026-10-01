import { afterEach, describe, expect, it } from "vitest";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import {
  forgetTranscriptViewport,
  parseTranscriptViewportState,
  persistTranscriptViewportSnapshot,
  setTranscriptViewportWindowLabelForTests,
  transcriptViewportSnapshot,
} from "./transcript-viewport-state.ts";
import { transcriptDisclosureKey } from "../transcript/presentation/transcript-disclosure-key.ts";

const TOOL = transcriptDisclosureKey.tool("tool-1");
const CARD = transcriptDisclosureKey.checkpoint("card");

afterEach(() => {
  resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
  setTranscriptViewportWindowLabelForTests("main");
});

describe("transcript viewport persistence", () => {
  it("isolates reading positions by window and session", () => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    setTranscriptViewportWindowLabelForTests("chat-a");
    persistTranscriptViewportSnapshot("s1", {
      position: { rowKey: "message-9", rowOffsetPx: 18 },
      openKeys: [TOOL, TOOL, "  ", "tool-1"],
    });
    expect(transcriptViewportSnapshot("s1")).toMatchObject({
      position: { rowKey: "message-9", rowOffsetPx: 18 },
      openKeys: [TOOL],
    });

    setTranscriptViewportWindowLabelForTests("chat-b");
    expect(transcriptViewportSnapshot("s1")).toBeUndefined();
    expect(
      getAppStateSnapshot().transcriptViewport?.byWindow["chat-a"],
    ).toBeDefined();
  });

  it("reads a position without a stable row as following the latest message", () => {
    expect(
      parseTranscriptViewportState({
        byWindow: {
          main: {
            bySession: {
              pixels: {
                position: { rowOffsetPx: 42 },
                openKeys: [],
                touchedAt: 1,
              },
              reading: {
                position: { rowKey: "m1", rowOffsetPx: 12 },
                openKeys: [CARD, "card"],
                touchedAt: 2,
              },
            },
          },
        },
      }),
    ).toEqual({
      byWindow: {
        main: {
          touchedAt: 2,
          bySession: {
            reading: {
              position: { rowKey: "m1", rowOffsetPx: 12 },
              openKeys: [CARD],
              touchedAt: 2,
            },
            pixels: { openKeys: [], touchedAt: 1 },
          },
        },
      },
    });
  });

  it("does not rewrite an unchanged position", () => {
    persistTranscriptViewportSnapshot("s1", {
      position: { rowKey: "message-9", rowOffsetPx: 18 },
      openKeys: [TOOL],
    });
    const first = getAppStateSnapshot().transcriptViewport;

    persistTranscriptViewportSnapshot("s1", {
      position: { rowKey: "message-9", rowOffsetPx: 18.25 },
      openKeys: [TOOL],
    });

    expect(getAppStateSnapshot().transcriptViewport).toBe(first);

    persistTranscriptViewportSnapshot("s1", { openKeys: [TOOL] });
    expect(transcriptViewportSnapshot("s1")?.position).toBeUndefined();
  });

  it("forgets a retired chat in every window and keeps other chats", () => {
    setTranscriptViewportWindowLabelForTests("chat-a");
    persistTranscriptViewportSnapshot("gone", { openKeys: [TOOL] });
    persistTranscriptViewportSnapshot("kept", { openKeys: [] });
    setTranscriptViewportWindowLabelForTests("chat-b");
    persistTranscriptViewportSnapshot("gone", { openKeys: [] });

    forgetTranscriptViewport("gone");

    const byWindow = getAppStateSnapshot().transcriptViewport?.byWindow;
    expect(Object.keys(byWindow ?? {})).toEqual(["chat-a"]);
    expect(Object.keys(byWindow?.["chat-a"]?.bySession ?? {})).toEqual(["kept"]);

    forgetTranscriptViewport("kept");
    expect(getAppStateSnapshot().transcriptViewport).toBeUndefined();
  });
});
