import { createSignal } from "solid-js";
import { languageExtensionForBuffer } from "../components/source/editor/codemirror-lang.ts";
import { createReadonlyTextEditor } from "../components/source/reader/readonly-text-editor.ts";
import { editorPrettyPref } from "../settings/editor/editor-prefs.ts";

/**
 * Literal evidence text in the Files read-only editor, as tall as its text.
 * It follows the editor's display settings and the Off, Wrap, Pretty choice.
 */
export function EvidenceText(props: { text: string; language?: string; label: string }) {
  const [host, setHost] = createSignal<HTMLDivElement>();
  createReadonlyTextEditor({
    parent: host, surface: "reader", text: () => props.text, label: props.label,
    language: () => (props.language ? languageExtensionForBuffer("", props.language) : undefined),
    pretty: () => ({ enabled: editorPrettyPref(), ...(props.language ? { languageHint: props.language } : {}) }),
  });
  return <div class="den-reader-fit den-briefing-evidence__editor" data-testid="briefing-evidence-text" ref={setHost} />;
}
