import { For, Show } from "solid-js";
import type { EditorContext, EditorContextFile } from "../../api/types.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";

function viewLabel(file: EditorContextFile): string {
  const view = file.view;
  if (!view) return "";
  if (view.cursor || view.start_line === view.end_line) return `:${view.start_line}`;
  return `:${view.start_line}-${view.end_line}`;
}

function viewTip(file: EditorContextFile): string {
  const view = file.view;
  const unsaved = file.unsaved ? ", unsaved" : "";
  if (!view) return `Open in the editor${unsaved}`;
  if (view.cursor) return `Cursor on line ${view.start_line}${unsaved}`;
  if (view.start_line === view.end_line) return `Line ${view.start_line} selected${unsaved}`;
  return `Lines ${view.start_line}-${view.end_line} selected${unsaved}`;
}

/** What the agent was told the person had open in the editor when they sent this message. */
export function EditorContextLine(props: { editor: EditorContext; projectId?: string }) {
  const open = (file: EditorContextFile) => {
    const projectId = props.projectId?.trim();
    if (!projectId) return;
    void openSourceLocation({
      intent: "permanent",
      projectId,
      rootId: file.root_id,
      path: file.path,
      entryKind: "file",
      line: file.view?.start_line,
      endLine: file.view && !file.view.cursor ? file.view.end_line : undefined,
    });
  };
  return (
    <p class="den-editor-context" data-testid="transcript-editor-context">
      <Show when={props.editor.files.length > 0} fallback={<span>Nothing open in the editor</span>}>
        <span class="den-editor-context__label">Open in the editor</span>
        <For each={props.editor.files}>
          {(file) => (
            <button
              type="button"
              class="den-inline-control den-editor-context__file"
              data-testid="transcript-editor-context-file"
              data-unsaved={file.unsaved ? "" : undefined}
              data-tip={viewTip(file)}
              aria-label={`Open ${file.display_path}${viewLabel(file)}`}
              onClick={() => open(file)}
            >
              {file.display_path}{viewLabel(file)}
              <Show when={file.unsaved}>
                <span class="den-editor-context__unsaved" aria-hidden="true">•</span>
              </Show>
            </button>
          )}
        </For>
        <Show when={props.editor.more_files > 0}>
          <span data-testid="transcript-editor-context-more">and {props.editor.more_files} more</span>
        </Show>
      </Show>
    </p>
  );
}
