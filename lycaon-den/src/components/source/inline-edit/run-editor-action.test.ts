import { describe, expect, it } from "vitest";
import { assistantProseAfterOrd } from "./assistant-prose.ts";

describe("assistantProseAfterOrd", () => {
  it("never reuses an assistant response from before the action", () => {
    expect(assistantProseAfterOrd([
      { role: "assistant", content: "stale response", ord: 4 },
      { role: "user", content: "new action", ord: 5 },
    ], 4)).toBeNull();
  });

  it("excludes the response belonging to a later user submission", () => {
    expect(assistantProseAfterOrd([
      { role: "assistant", content: "action response", ord: 6 },
      { role: "user", origin: "host", content: "host wake", ord: 7 },
      { role: "assistant", content: "final action response", ord: 8 },
      { role: "user", origin: "user", content: "another request", ord: 9 },
      { role: "assistant", content: "unrelated response", ord: 10 },
    ], 5)).toBe("final action response");
  });

  it("returns the newest assistant response after the action boundary", () => {
    expect(assistantProseAfterOrd([
      { role: "assistant", content: "stale response", ord: 4 },
      { role: "assistant", content: " first response ", ord: 6 },
      { role: "assistant", content: " final response ", ord: 8 },
    ], 4)).toBe("final response");
  });
});
