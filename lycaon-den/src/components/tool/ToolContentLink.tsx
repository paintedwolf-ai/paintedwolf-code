import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { ChatContentLink } from "../transcript/ChatContentLink.tsx";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { useSourceContext } from "../source/annotations/source-context.ts";
import { createEffect, createMemo, untrack } from "solid-js";
import { findController } from "../../find/find-controller.ts";

export function ToolContentLink(props: {
  pane: string;
  label: string;
  content: ChatContentDocument["content"];
  projectId?: string;
  messageId?: string;
  revealOffset?: number;
}) {
  const tool = useToolContent();
  const source = useSourceContext();
  const projectId = () => props.projectId ?? tool?.identity().projectId ?? source?.projectId;
  const searchable = createMemo(() => props.content.kind === "inline" && !!projectId());
  const document = (): ChatContentDocument | undefined => {
    const identity = tool?.identity();
    if (!identity) return;
    return {
      kind: "tool",
      sessionId: identity.sessionId,
      toolCallId: identity.toolCallId,
      messageId: props.messageId ?? identity.messageId,
      pane: props.pane,
      title: `${props.label} · ${identity.title ?? "Tool"}`,
      content: props.content,
      revealOffset: props.revealOffset,
    };
  };
  const open = () => {
    const value = document(), project = projectId();
    if (value && project) openFilesSurface({ kind: "chat-content", projectId: project, document: value });
  };
  createEffect(() => {
    if (props.revealOffset !== undefined && findController.isOpen()) untrack(open);
  });
  return <ChatContentLink searchable={searchable()} label={props.label} projectId={projectId()} document={document} testId="tool-content-link" />;
}
