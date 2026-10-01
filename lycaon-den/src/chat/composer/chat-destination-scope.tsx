/** The chat that Add to chat actions inside a surface stage into. */

import { createContext, useContext, type Accessor, type ParentProps } from "solid-js";
import type { ChatDestination } from "./shared-composer-document.ts";

const ChatDestinationScopeContext = createContext<Accessor<ChatDestination | undefined>>(
  () => undefined,
);

/** Exact destination from trimmed identities; undefined when either is missing. */
export function chatDestinationOf(
  projectId: string | null | undefined,
  sessionId: string | null | undefined,
): ChatDestination | undefined {
  const project = projectId?.trim() ?? "";
  const session = sessionId?.trim() ?? "";
  return project && session ? { projectId: project, sessionId: session } : undefined;
}

/** Rows keep their source session; a worker's rows belong to the chat that dispatched it. */
export function ChatDestinationScope(
  props: ParentProps<{ destination: ChatDestination | undefined }>,
) {
  return (
    <ChatDestinationScopeContext.Provider value={() => props.destination}>
      {props.children}
    </ChatDestinationScopeContext.Provider>
  );
}

/** Undefined outside a scope, or in one without a chat; Add to chat then asks. */
export function useChatDestinationScope(): Accessor<ChatDestination | undefined> {
  return useContext(ChatDestinationScopeContext);
}
