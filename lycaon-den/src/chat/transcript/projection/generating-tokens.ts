import type { Message } from "../../../api/types.ts";

/** Compact token count for live heartbeats: 512 → "512", 1536 → "1.5k". */
export function formatGeneratingTokenCount(tokens: number): string {
  if (tokens < 1000) return String(tokens);
  return `${(tokens / 1000).toFixed(1)}k`;
}

/** The host estimate includes visible content and hidden reasoning. */
export function generatingTokensLabel(tokens: number | null | undefined): string | null {
  if (!tokens || tokens <= 0) return null;
  return `generating ~${formatGeneratingTokenCount(tokens)} tokens`;
}

/** Live token estimate for a wire message, or 0 unless it is actively streaming. */
export function messageGeneratingTokens(
  msg: Pick<Message, "status" | "generating_tokens">,
): number {
  if (msg.status !== "streaming") return 0;
  return msg.generating_tokens ?? 0;
}

/** Latest positive token estimate among streaming messages. */
export function liveGeneratingTokensFromMessages(
  messages: readonly Pick<Message, "status" | "generating_tokens">[] | undefined,
): number {
  if (!messages) return 0;
  for (let i = messages.length - 1; i >= 0; i--) {
    const msg = messages[i];
    if (!msg) continue;
    const tokens = messageGeneratingTokens(msg);
    if (tokens > 0) return tokens;
  }
  return 0;
}
