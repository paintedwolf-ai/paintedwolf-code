import { Show } from "solid-js";

import { redactionSummary, type RedactionSummary } from "../../chat/transcript/content/redaction-spans.ts";
import type { Message } from "../../api/types.ts";

/** Summarizes the redaction spans recorded for a tool result. */
export function RedactionChicklet(props: { message: Pick<Message, "host_secret_redaction"> | undefined }) {
  const summary = () => redactionSummary(props.message);
  return (
    <Show when={summary()} keyed>
      {(found) => (
        <span class="den-redaction-chicklet" data-tip={redactionTitle(found.titles)}>
          {label(found)}
        </span>
      )}
    </Show>
  );
}

function label({ secrets, references, masked }: RedactionSummary): string {
  const kinds = [secrets, references, masked].filter((count) => count > 0).length;
  if (kinds > 1) return `${secrets + references + masked} hidden`;
  if (secrets > 0) return secrets === 1 ? "1 secret removed" : `${secrets} secrets removed`;
  if (references > 0) return references === 1 ? "1 secret protected" : `${references} secrets protected`;
  return masked === 1 ? "1 value not shown" : `${masked} values not shown`;
}

function redactionTitle(titles: string[]): string {
  const named = titles.length ? titles.join(", ") : "Detected credential";
  return `${named}. Your file on disk is unchanged.`;
}
