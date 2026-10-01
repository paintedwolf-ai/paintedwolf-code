import { Show, onCleanup, onMount } from "solid-js";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { unifiedMergeView } from "@codemirror/merge";
import { editorCspNonce } from "../../components/source/editor/editor-csp-nonce.ts";
import { lineGutterExtension } from "../../components/source/annotations/line-gutter.ts";
import type { BufferMergeModel } from "../documents/buffer-merge.ts";
import { mergeApplyPayload } from "../documents/buffer-merge.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";

type Props = {
  model: BufferMergeModel;
  onApply: (payload: ReturnType<typeof mergeApplyPayload>) => void | Promise<void>;
  onCancel: () => void;
  applyNote?: string | null;
  applying?: boolean;
};

// Reuse one theme to avoid per-mount stylesheet injection.
const mergeHostTheme = EditorView.theme({
  "&": { height: "100%" },
  ".cm-scroller": { overflow: "auto" },
});

/** Editable merge view with chunk controls. */
export function BufferMergeView(props: Props) {
  let host: HTMLDivElement | undefined;
  let view: EditorView | undefined;

  onMount(() => {
    if (!host) return;
    const state = EditorState.create({
      doc: props.model.mine,
      extensions: [
        editorCspNonce(),
        history(),
        keymap.of([
          ...defaultKeymap,
          ...historyKeymap,
          {
            key: "Mod-Enter",
            run: () => {
              void submit();
              return true;
            },
          },
        ]),
        lineGutterExtension,
        unifiedMergeView({
          original: props.model.theirs,
          mergeControls: true,
          // Conflict diffs do not show provenance.
          gutter: false,
          // Bound synchronous diff work for large divergent inputs.
          diffConfig: { scanLimit: 500, timeout: 250 },
        }),
        mergeHostTheme,
        EditorView.editable.of(true),
      ],
    });
    view = new EditorView({ state, parent: host });
  });

  onCleanup(() => {
    view?.destroy();
    view = undefined;
  });

  const submit = async () => {
    if (!view) return;
    const merged = view.state.doc.toString();
    await props.onApply(mergeApplyPayload(props.model, merged));
  };

  return (
    <div class="den-files-merge" data-testid="files-buffer-merge">
      <div class="den-files-merge__header">
        <span>{props.model.comparison === "retained" ? "Current edits vs. a retained draft" : "Your unsaved edits vs. the version on disk"}</span>
        <Show when={props.applyNote}>
          <span class="den-files-merge__note" role="status" data-testid="files-buffer-merge-note">
            {props.applyNote}
          </span>
        </Show>
      </div>
      <div class="den-files-merge__host" data-testid="files-buffer-merge-host" ref={host} />
      <div class="den-files-merge__footer">
        <DenButton
          variant="primary"
          compact
          data-testid="files-buffer-merge-apply"
          disabled={props.applying}
          onClick={() => void submit()}
        >
          Apply
        </DenButton>
        <DenButton
          variant="ghost"
          compact
          data-testid="files-buffer-merge-cancel"
          disabled={props.applying}
          onClick={() => props.onCancel()}
        >
          Cancel
        </DenButton>
        <span class="den-files-merge__hint">
          Accept or reject chunks inline, then Apply.
        </span>
      </div>
    </div>
  );
}
