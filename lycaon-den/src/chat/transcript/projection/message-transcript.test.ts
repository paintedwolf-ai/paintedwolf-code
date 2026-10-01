import { describe, expect, it } from "vitest";
import {
  hasDenTranscriptMessages,
  isDenTranscriptMessage,
  isInternalTranscriptMessage,
  isInternalTranscriptUserMessage,
  isWorkflowBoundaryMessage,
} from "./message-transcript.ts";

describe("message-transcript", () => {
  it("treats transcript-visible workflow_boundary as Den chat", () => {
    expect(
      isWorkflowBoundaryMessage({
        id: "b1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        created_at: "t",
      }),
    ).toBe(true);
    expect(
      isDenTranscriptMessage({
        id: "b1",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "transcript",
        workflow_boundary: { event: "started", workflow_id: "plan" },
        created_at: "t",
      }),
    ).toBe(true);
    expect(
      isDenTranscriptMessage({
        id: "b-ambient",
        role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
        content: "",
        kind: "workflow_boundary",
        visibility: "internal",
        workflow_boundary: { event: "started", workflow_id: "implement" },
        created_at: "t",
      }),
    ).toBe(false);
  });

  it("keeps internal user kicks out of Den but not boundary-shaped rows", () => {
    expect(
      isInternalTranscriptMessage({
        id: "k1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "host kick",
        visibility: "internal",
        created_at: "t",
      }),
    ).toBe(true);
    expect(
      isDenTranscriptMessage({
        id: "k1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "host kick",
        visibility: "internal",
        created_at: "t",
      }),
    ).toBe(false);
  });

  it("isInternalTranscriptUserMessage uses wire visibility only", () => {
    expect(
      isInternalTranscriptUserMessage({
        id: "u1",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "kick",
        visibility: "internal",
        created_at: "t",
      }),
    ).toBe(true);
    expect(
      isInternalTranscriptUserMessage({
        id: "u2",
        role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
        content: "Build a game",
        created_at: "t",
      }),
    ).toBe(false);
    expect(
      isInternalTranscriptUserMessage({
        id: "u3",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "hi",
        visibility: "internal",
        created_at: "t",
      }),
    ).toBe(false);
  });

  it("does not count an ambient workflow boundary as user-visible chat", () => {
    const ambientBoundary = {
      id: "b-ambient",
      role: "system" as const,
      origin: "host" as const,
      authority: "system" as const,
      trust_tier: "trusted" as const,
      content: "",
      kind: "workflow_boundary" as const,
      visibility: "internal" as const,
      workflow_boundary: { event: "started" as const, workflow_id: "implement" },
      created_at: "t",
    };
    expect(hasDenTranscriptMessages([ambientBoundary])).toBe(false);
    expect(
      hasDenTranscriptMessages([
        ambientBoundary,
        {
          id: "u-visible",
          role: "user",
          origin: "user",
          authority: "user",
          trust_tier: "trusted",
          content: "Build it",
          visibility: "transcript",
          created_at: "t",
        },
      ]),
    ).toBe(true);
  });

});
