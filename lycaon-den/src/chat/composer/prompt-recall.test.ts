import { beforeEach, describe, expect, it } from "vitest";
import {
  PROMPT_RECALL_CAP,
  caretAtRecallEdge,
  exitRecall,
  inRecall,
  recordSentPrompt,
  resetPromptRecallForTests,
  walkRecall,
} from "./prompt-recall.ts";

beforeEach(resetPromptRecallForTests);

describe("prompt recall ring", () => {
  it("walks back through what was actually sent, newest first", () => {
    recordSentPrompt("s1", "first");
    recordSentPrompt("s1", "second");
    expect(walkRecall("s1", "older")).toBe("second");
    expect(walkRecall("s1", "older")).toBe("first");
  });

  it("stops at the oldest instead of wrapping", () => {
    recordSentPrompt("s1", "only");
    expect(walkRecall("s1", "older")).toBe("only");
    // Wrapping around to the newest would make ↑ feel like it lost your place.
    expect(walkRecall("s1", "older")).toBeNull();
  });

  it("walks forward and back out to the empty composer", () => {
    recordSentPrompt("s1", "first");
    recordSentPrompt("s1", "second");
    walkRecall("s1", "older");
    walkRecall("s1", "older");
    expect(walkRecall("s1", "newer")).toBe("second");
    // "" is the empty composer they started from — the way out of recall that
    // does not require deleting characters.
    expect(walkRecall("s1", "newer")).toBe("");
    expect(inRecall("s1")).toBe(false);
    expect(walkRecall("s1", "newer")).toBeNull();
  });

  it("does nothing for a session with no history", () => {
    expect(walkRecall("fresh", "older")).toBeNull();
  });

  it("keeps rings separate per session", () => {
    recordSentPrompt("s1", "mine");
    recordSentPrompt("s2", "theirs");
    expect(walkRecall("s1", "older")).toBe("mine");
    expect(walkRecall("s2", "older")).toBe("theirs");
  });

  it("typing exits recall and resets the cursor", () => {
    recordSentPrompt("s1", "first");
    recordSentPrompt("s1", "second");
    walkRecall("s1", "older");
    expect(inRecall("s1")).toBe(true);
    exitRecall("s1");
    expect(inRecall("s1")).toBe(false);
    // Next ↑ starts from the newest again.
    expect(walkRecall("s1", "older")).toBe("second");
  });

  it("does not fill the ring with repeats of the same prompt", () => {
    recordSentPrompt("s1", "again");
    recordSentPrompt("s1", "again");
    expect(walkRecall("s1", "older")).toBe("again");
    expect(walkRecall("s1", "older")).toBeNull();
  });

  it("ignores blank sends and caps the ring", () => {
    recordSentPrompt("s1", "   ");
    expect(walkRecall("s1", "older")).toBeNull();
    for (let i = 0; i < PROMPT_RECALL_CAP + 5; i++) recordSentPrompt("s1", `p${i}`);
    let steps = 0;
    while (walkRecall("s1", "older") !== null) steps++;
    expect(steps).toBe(PROMPT_RECALL_CAP);
  });

  it("sending records the prompt so it is recallable next time", () => {
    recordSentPrompt("s1", "  padded  ");
    expect(walkRecall("s1", "older")).toBe("padded");
  });
});

describe("caret at the recall edge", () => {
  it("walks older only from the first line", () => {
    expect(caretAtRecallEdge("one\ntwo", 2, 2, "older")).toBe(true);
    // Line two: this ↑ is a caret move inside a recalled prompt.
    expect(caretAtRecallEdge("one\ntwo", 5, 5, "older")).toBe(false);
  });

  it("walks newer only from the last line", () => {
    expect(caretAtRecallEdge("one\ntwo", 5, 5, "newer")).toBe(true);
    expect(caretAtRecallEdge("one\ntwo", 2, 2, "newer")).toBe(false);
  });

  it("treats an empty composer as both edges", () => {
    expect(caretAtRecallEdge("", 0, 0, "older")).toBe(true);
    expect(caretAtRecallEdge("", 0, 0, "newer")).toBe(true);
  });

  it("never walks while text is selected", () => {
    // Shift+↑ extends a selection; replacing it with history would eat the edit.
    expect(caretAtRecallEdge("one\ntwo", 0, 3, "older")).toBe(false);
    expect(caretAtRecallEdge("one\ntwo", 4, 7, "newer")).toBe(false);
  });
});
