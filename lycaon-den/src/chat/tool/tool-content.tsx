import { createContext, useContext, type Accessor } from "solid-js";
import type { ChatContentAccess } from "../transcript/content/chat-content-reader.ts";

type ToolContent = {
  identity: Accessor<{
    sessionId: string;
    messageId: string;
    toolCallId: string;
    projectId?: string;
    title?: string;
  }>;
  output: Accessor<ChatContentAccess | undefined>;
  args: Accessor<ChatContentAccess | undefined>;
  outputOffset: Accessor<number | undefined>;
  argsOffset: Accessor<number | undefined>;
};
const ToolContentContext = createContext<ToolContent>();
export const ToolContentProvider = ToolContentContext.Provider;
export const useToolContent = () => useContext(ToolContentContext);
