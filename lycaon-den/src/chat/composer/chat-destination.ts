/** Imperative, machine-state-only chooser for an exact chat destination. */

import { createSignal } from "solid-js";
import type { ChatDestination } from "./shared-composer-document.ts";

export type ChatDestinationRequest = {
  /** References may only target a chat in this project. */
  projectId?: string;
  /** Highlighted convenience candidate; never silently treated as authority. */
  suggested?: ChatDestination;
};

export type PendingChatDestinationRequest = ChatDestinationRequest & {
  id: string;
};

const [pending, setPending] = createSignal<
  PendingChatDestinationRequest | null
>(null);
let settlePending: ((destination: ChatDestination | null) => void) | undefined;
let openDestination: ((destination: ChatDestination) => Promise<void>) | undefined;

export function registerChatDestinationOpenSink(
  next: (destination: ChatDestination) => Promise<void>,
): () => void {
  openDestination = next;
  return () => {
    if (openDestination === next) openDestination = undefined;
  };
}

/** Open newly created destinations through the shell's session lifecycle. */
export async function openChatDestination(destination: ChatDestination): Promise<void> {
  await openDestination?.(destination);
}

export function pendingChatDestinationRequest(): PendingChatDestinationRequest | null {
  return pending();
}

export function chooseChatDestination(
  request: ChatDestinationRequest,
): Promise<ChatDestination | null> {
  settlePending?.(null);
  return new Promise((resolve) => {
    settlePending = resolve;
    setPending({
      ...request,
      projectId: request.projectId?.trim() || undefined,
      id: crypto.randomUUID(),
    });
  });
}

export function settleChatDestination(
  destination: ChatDestination | null,
): void {
  const settle = settlePending;
  settlePending = undefined;
  setPending(null);
  settle?.(destination);
}

export function resetChatDestinationForTests(): void {
  settlePending?.(null);
  settlePending = undefined;
  setPending(null);
}
