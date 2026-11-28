import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { ChatContentLink } from "../transcript/ChatContentLink.tsx";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { useSourceContext } from "../source/annotations/source-context.ts";
import { createEffect, createMemo, untrack } from "solid-js";
import { findController } from "../../find/find-controller.ts";

type ToolIdentity = { sessionId: string; messageId: string; toolCallId: string; title?: string };

/** The Files document a tool's link opens; one pane per call and kind. */
export function toolContentDocument(identity: ToolIdentity, link: {
  pane: string; label: string; content: ChatContentDocument["content"];
  messageId?: string; toolCallId?: string; revealOffset?: number; path?: string; language?: string;
}): ChatContentDocument {
  return {
    kind: "tool",
    sessionId: identity.sessionId,
    toolCallId: link.toolCallId ?? identity.toolCallId,
    messageId: link.messageId ?? identity.messageId,
    pane: link.pane,
    title: `${link.label} · ${identity.title ?? "Tool"}`,
    content: link.content,
    revealOffset: link.revealOffset,
    ...(link.path ? { path: link.path } : {}),
    ...(link.language ? { language: link.language } : {}),
  };
}

export function ToolContentLink(props: {
  pane: string;
  label: string;
  content: ChatContentDocument["content"];
  projectId?: string;
  messageId?: string;
  toolCallId?: string;
  revealOffset?: number;
  testId?: string;
  path?: string;
  language?: string;
  short?: boolean;
}) {
  const tool = useToolContent();
  const source = useSourceContext();
  const projectId = () => props.projectId ?? tool?.identity().projectId ?? source?.projectId;
  const searchable = createMemo(() => props.content.kind === "inline" && !!projectId());
  const document = (): ChatContentDocument | undefined => {
    const identity = tool?.identity();
    return identity ? toolContentDocument(identity, props) : undefined;
  };
  const open = () => {
    const value = document(), project = projectId();
    if (value && project) openFilesSurface({ kind: "chat-content", projectId: project, document: value });
  };
  createEffect(() => {
    if (props.revealOffset !== undefined && findController.isOpen()) untrack(open);
  });
  return <ChatContentLink searchable={searchable()} label={props.label} projectId={projectId()} document={document} testId={props.testId ?? "tool-content-link"} short={props.short} />;
}
