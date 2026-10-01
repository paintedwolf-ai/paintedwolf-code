import { Show } from "solid-js";
import { useToolContent } from "../../chat/tool/tool-content.tsx";
import { ToolContentLink } from "./ToolContentLink.tsx";

export function RetainedToolContent(props: { field: "output" | "args" }) {
  const content = useToolContent();
  const access = () => props.field === "output" ? content?.output() : content?.args();
  return <Show when={access()} keyed>{reader => <ToolContentLink pane={props.field} messageId={reader.messageId} content={{ kind: "retained", reference: reader.reference }}
    revealOffset={props.field === "output" ? content?.outputOffset() : content?.argsOffset()}
    label={props.field === "output" ? "Output" : "Input"} />}</Show>;
}
