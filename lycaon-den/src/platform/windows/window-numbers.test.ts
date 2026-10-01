import { describe, expect, it } from "vitest";
import {
  MAIN_WINDOW_LABEL,
  currentWindowNumber,
  nextWindowLabel,
  windowLabelForNumber,
} from "./window-numbers.ts";
import type { WindowSubject } from "./window-subject.ts";

const peers = [
  { label: "session:s1:2", viewNumber: 2 },
  { label: "file:0123abcd:5", viewNumber: 5 },
  { label: "context:files:3", viewNumber: 3 },
];

const peerSubject = (viewId: string): WindowSubject => ({
  kind: "session",
  projectId: "p1",
  sessionId: "s1",
  viewId,
});

describe("window numbers", () => {
  it("main is Window 1", () => {
    expect(currentWindowNumber(null)).toBe(1);
    expect(windowLabelForNumber(peers, 1)).toBe(MAIN_WINDOW_LABEL);
  });

  it("a peer's number comes from its native subject", () => {
    expect(currentWindowNumber(peerSubject("3"))).toBe(3);
  });

  it("jumps to a peer by its native number", () => {
    expect(windowLabelForNumber(peers, 5)).toBe("file:0123abcd:5");
    expect(windowLabelForNumber(peers, 4)).toBeNull();
  });

  it("cycles from each window to the next by number, using native labels", () => {
    expect(nextWindowLabel(peers, currentWindowNumber(null))).toBe("session:s1:2");
    expect(nextWindowLabel(peers, currentWindowNumber(peerSubject("2")))).toBe(
      "context:files:3",
    );
    expect(nextWindowLabel(peers, currentWindowNumber(peerSubject("3")))).toBe(
      "file:0123abcd:5",
    );
    expect(nextWindowLabel(peers, currentWindowNumber(peerSubject("5")))).toBe(
      MAIN_WINDOW_LABEL,
    );
  });

  it("has nowhere to cycle without peers", () => {
    expect(nextWindowLabel([], 1)).toBeNull();
  });
});
