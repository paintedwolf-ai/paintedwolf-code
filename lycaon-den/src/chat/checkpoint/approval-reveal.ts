import { revealChicklet } from "../transcript/presentation/transcript-reveal.ts";

export type ApprovalRevealRequest = {
  toolCallId: string;
  sessionId: string;
};

type ApprovalRevealSink = (req: ApprovalRevealRequest) => void;

let sink: ApprovalRevealSink | null = null;

export function registerApprovalRevealSink(next: ApprovalRevealSink): () => void {
  sink = next;
  return () => {
    if (sink === next) sink = null;
  };
}

export function revealApprovalOrigin(req: ApprovalRevealRequest): void {
  const toolCallId = req.toolCallId.trim();
  if (!toolCallId) return;
  if (sink) {
    sink({ toolCallId, sessionId: req.sessionId.trim() });
    return;
  }
  void revealChicklet(
    () =>
      typeof document === "undefined"
        ? null
        : document.querySelector<HTMLElement>(".den-chat-stream"),
    {
      sessionId: req.sessionId.trim(),
      anchor: { chicklet: "tool", anchorId: toolCallId },
    },
  );
}
