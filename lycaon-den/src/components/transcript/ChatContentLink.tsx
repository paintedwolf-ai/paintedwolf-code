import { findController } from "../../find/find-controller.ts";
import { createSignal } from "solid-js";
import { bindFindRevealHost } from "../../find/use-findable-view.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";

/** Opening and Find prepare the document text on demand. */
export function ChatContentLink(props: {
  label: string;
  projectId?: string;
  document: () => ChatContentDocument | undefined;
  testId?: string;
  searchable?: boolean;
}) {
  const findId = `chat-content-link:${crypto.randomUUID()}`;
  const [host, setHost] = createSignal<HTMLButtonElement>();
  const open = (forFind = false) => {
    const document = props.document();
    const match = forFind ? findController.matches()[findController.activeIndex()] : undefined;
    if (props.projectId && document) openFilesSurface({ kind: "chat-content", projectId: props.projectId,
      document: forFind ? { ...document, find: { query: findController.query(), caseSensitive: findController.caseSensitive(), offset: match?.revealHostId === findId ? match.startOffset : 0 } } : document });
  };
  bindFindRevealHost({
    id: findId, hostEl: () => props.searchable ? host() : undefined,
    isCollapsed: () => true, revealForFind: () => { open(true); return () => {}; },
    collapsedCorpus: () => {
      const content = props.document()?.content;
      return content?.kind === "inline" ? content.text : "";
    },
    collapsedCorpusAnchor: host,
  });
  return <button ref={setHost} type="button" class="den-chat-content-link" data-testid={props.testId ?? "chat-content-link"}
    disabled={!props.projectId} onClick={() => open()}>
    {props.label} in Files
  </button>;
}
