import { describe, expect, it } from "vitest";
import type { Message } from "../../../api/types.ts";
import {
  formatGeneratingTokenCount,
  generatingTokensLabel,
  liveGeneratingTokensFromMessages,
  messageGeneratingTokens,
} from "./generating-tokens.ts";

describe("generating-tokens", () => {
  it("formats compact counts", () => {
    expect(formatGeneratingTokenCount(512)).toBe("512");
    expect(formatGeneratingTokenCount(1536)).toBe("1.5k");
    expect(formatGeneratingTokenCount(24876)).toBe("24.9k");
  });

  it("labels only when generating", () => {
    expect(generatingTokensLabel(0)).toBeNull();
    expect(generatingTokensLabel(undefined)).toBeNull();
    expect(generatingTokensLabel(2048)).toBe("generating ~2.0k tokens");
  });

  it("counts only actively streaming rows", () => {
    const streaming: Pick<Message, "status" | "generating_tokens"> = {
      status: "streaming",
      generating_tokens: 900,
    };
    const settled: Pick<Message, "status" | "generating_tokens"> = {
      status: "complete",
      generating_tokens: 900,
    };
    expect(messageGeneratingTokens(streaming)).toBe(900);
    expect(messageGeneratingTokens(settled)).toBe(0);
  });

  it("finds the live count in a message list, else 0", () => {
    const rows: Pick<Message, "status" | "generating_tokens">[] = [
      { status: "complete", generating_tokens: 100 },
      { status: "streaming", generating_tokens: 4096 },
    ];
    expect(liveGeneratingTokensFromMessages(rows)).toBe(4096);
    expect(liveGeneratingTokensFromMessages([{ status: "complete" }])).toBe(0);
    expect(liveGeneratingTokensFromMessages(undefined)).toBe(0);
  });
});
