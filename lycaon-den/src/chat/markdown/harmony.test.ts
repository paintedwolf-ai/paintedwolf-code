import { describe, expect, it } from "vitest";
import { peelFinalEnvelope } from "./harmony.ts";

describe("peelFinalEnvelope", () => {
  const envelope = '{"leg_status":"complete","brief":"done"}';

  it("peels Harmony role and final prefixes", () => {
    expect(peelFinalEnvelope(envelope)).toBe(envelope);
    expect(peelFinalEnvelope(`final${envelope}`)).toBe(envelope);
    expect(peelFinalEnvelope(`assistantfinal${envelope}`)).toBe(envelope);
    expect(peelFinalEnvelope(`assistantassistant${envelope}`)).toBe(envelope);
  });

  it("leaves ordinary prose and hybrid dumps unchanged", () => {
    expect(peelFinalEnvelope(`Summary\n${envelope}`)).toBe(`Summary\n${envelope}`);
    expect(peelFinalEnvelope("analysis of the heap")).toBe("analysis of the heap");
  });
});
