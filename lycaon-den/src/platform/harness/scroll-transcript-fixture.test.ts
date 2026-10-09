import { expect, it } from "vitest";
import { scrollTranscriptFixture } from "./scroll-transcript-fixture.ts";

it("seeds varied user and assistant history with explicit provenance", () => {
  const rows = scrollTranscriptFixture(6, 5);
  expect(rows).toHaveLength(12);
  expect(new Set(rows.map(row => row.id)).size).toBe(12);
  expect(rows[0]).toMatchObject({ role: "user", origin: "user", authority: "user", trust_tier: "trusted", content: "Part 6: walk me through the next step." });
  expect(rows.filter(row => row.role === "assistant").every(row => row.origin === "model" && row.authority === "none")).toBe(true);
  expect(rows.some(row => row.content?.includes("Checklist"))).toBe(true);
  expect(rows.some(row => row.content?.includes("```sh"))).toBe(true);
  expect(rows.some(row => row.content?.includes("Step"))).toBe(true);
});

it("ends the short fixture in a completed tool activity with durable call links", () => {
  const rows = scrollTranscriptFixture(5, 0, true);
  const calls = rows.find(row => row.tool_calls?.length);
  expect(calls?.tool_calls).toHaveLength(3);
  const results = rows.filter(row => row.role === "tool");
  expect(results).toHaveLength(3);
  for (const result of results) {
    expect(result).toMatchObject({ origin: "tool", authority: "none", trust_tier: "untrusted", tool_result: { assistant_message_id: calls!.id, outcome: "completed" } });
    expect(calls!.tool_calls!.some(call => call.id === result.tool_result!.tool_call_id)).toBe(true);
    expect(result.tool_result!.content).toBeTruthy();
  }
  expect(rows.at(-1)?.content).toBe("Done.");
});

it("rejects invalid fixture sizes and offsets before admission", () => {
  for (const count of [0, 201, 1.5, NaN]) expect(() => scrollTranscriptFixture(count)).toThrow();
  expect(() => scrollTranscriptFixture(1, -1)).toThrow();
  expect(scrollTranscriptFixture(200, 0, true).length).toBeLessThan(1000);
});
