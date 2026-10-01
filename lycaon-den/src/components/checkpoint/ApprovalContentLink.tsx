import { createContext, useContext, type Accessor } from "solid-js";
import { ChatContentLink } from "../transcript/ChatContentLink.tsx";
import { useSourceContext } from "../source/annotations/source-context.ts";

type ApprovalContentIdentity = { checkpointId: string; sessionId?: string | null; projectId?: string };
const ApprovalContentContext = createContext<Accessor<ApprovalContentIdentity>>();
export const ApprovalContentProvider = ApprovalContentContext.Provider;

export function ApprovalContentLink(props: {
  label: string;
  pane: string;
  title?: string;
  text: () => string;
  identity?: ApprovalContentIdentity;
}) {
  const context = useContext(ApprovalContentContext);
  const source = useSourceContext();
  const identity = () => props.identity ?? context?.();
  return <ChatContentLink searchable label={props.label} projectId={identity()?.projectId ?? source?.projectId}
    document={() => {
      const address = identity();
      if (!address) return;
      return { kind: "approval", checkpointId: address.checkpointId, sessionId: address.sessionId ?? "",
        pane: props.pane, title: props.title ?? props.label, content: { kind: "inline", text: props.text() } };
    }} />;
}
