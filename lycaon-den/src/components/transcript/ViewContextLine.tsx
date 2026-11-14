import { For, Show } from "solid-js";
import type { ViewContext, ViewLocation, ViewSelection } from "../../api/types.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";

type Target = { rootId: string; path: string; label: string; tip: string; kind: "file" | "folder"; line?: number; endLine?: number };

function selectionSuffix(selection: ViewSelection | undefined): string {
  if (!selection) return "";
  if (selection.cursor || selection.start_line === selection.end_line) return `:${selection.start_line}`;
  return `:${selection.start_line}-${selection.end_line}`;
}

function locationTarget(location: ViewLocation, tip: string): Target {
  const suffix = location.line ? `:${location.line}` : "";
  return { rootId: location.root_id, path: location.path, label: location.display_path + suffix, tip, kind: "file", line: location.line };
}

/** Every place the line names, in the order the agent was told. */
export function viewTargets(view: ViewContext): Target[] {
  const targets: Target[] = view.editors.map((editor) => ({
    rootId: editor.root_id, path: editor.path, kind: "file",
    label: editor.display_path + selectionSuffix(editor.selection),
    tip: `In an editor${editor.unsaved ? ", unsaved" : ""}`,
    line: editor.selection?.start_line,
    endLine: editor.selection && !editor.selection.cursor ? editor.selection.end_line : undefined,
  }));
  for (const diff of view.diffs) targets.push({ rootId: diff.root_id, path: diff.path, label: diff.display_path, tip: "In a diff", kind: "file" });
  if (view.folder) targets.push({ rootId: view.folder.root_id, path: view.folder.path, label: view.folder.display_path, tip: "Folder last opened in the file tree", kind: "folder" });
  if (view.search?.hit) targets.push(locationTarget(view.search.hit, "Selected search result"));
  for (const finding of view.findings) {
    if (finding.location) targets.push(locationTarget(finding.location, `Security finding ${finding.rule_id}`));
  }
  return targets;
}

/** What the agent was told the person had in view when they sent this message. */
export function ViewContextLine(props: { view: ViewContext; projectId?: string }) {
  const targets = () => viewTargets(props.view);
  const findingsWithoutPlace = () => props.view.findings.filter((finding) => !finding.location);
  const open = (target: Target) => {
    const projectId = props.projectId?.trim();
    if (!projectId) return;
    void openSourceLocation({
      intent: "permanent", projectId, rootId: target.rootId, path: target.path,
      entryKind: target.kind, line: target.line, endLine: target.endLine,
    });
  };
  const empty = () => targets().length === 0 && !props.view.search && findingsWithoutPlace().length === 0;
  return (
    <p class="den-view-context" data-testid="transcript-view-context">
      <Show when={!empty()} fallback={<span>Nothing in view</span>}>
        <span class="den-view-context__label">In view</span>
        <For each={targets()}>
          {(target) => (
            <button
              type="button"
              class="den-inline-control den-view-context__item"
              data-testid="transcript-view-context-item"
              data-tip={target.tip}
              aria-label={`Open ${target.label}`}
              onClick={() => open(target)}
            >
              {target.label}
            </button>
          )}
        </For>
        <Show when={props.view.search} keyed>
          {(search) => <span data-testid="transcript-view-context-search" data-tip="Search">“{search.query}”</span>}
        </Show>
        <For each={findingsWithoutPlace()}>
          {(finding) => <span data-testid="transcript-view-context-finding" data-tip="Security finding">{finding.rule_id}</span>}
        </For>
      </Show>
    </p>
  );
}
