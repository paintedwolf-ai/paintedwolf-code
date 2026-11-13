import { createContext, useContext, type Accessor } from "solid-js";
import type { RetainedChatContentAccess } from "../transcript/content/chat-content-reader.ts";

type ToolContent = {
  identity: Accessor<{
    sessionId: string;
    messageId: string;
    toolCallId: string;
    projectId?: string;
    title?: string;
  }>;
  output: Accessor<RetainedChatContentAccess | undefined>;
  args: Accessor<RetainedChatContentAccess | undefined>;
  outputOffset: Accessor<number | undefined>;
  argsOffset: Accessor<number | undefined>;
  revealedCallId?: Accessor<string | undefined>;
};
const ToolContentContext = createContext<ToolContent>();
export const ToolContentProvider = ToolContentContext.Provider;
export const useToolContent = () => useContext(ToolContentContext);
