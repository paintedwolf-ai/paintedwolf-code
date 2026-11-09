import { For, Show, createSignal } from "solid-js";
import type { EditorDocumentChange } from "../../api/types.ts";
import { DenButton } from "../../components/primitives/DenButton.tsx";
import { TablePager } from "../../components/list/TablePager.tsx";
import { personLabel } from "../../components/source/annotations/source-contributor-label.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";

const pageSize = 5;

function changeAuthorLabel(change: EditorDocumentChange): string {
  switch (change.actor_kind) {
    case "agent": return `From ${change.session_title || "agent"}${change.turn ? ` · turn ${change.turn}` : ""}`;
    case "user": return personLabel(change.person_id);
    case "restore": return "Restored content";
  }
}

export function DocumentEditGroup(props: {
  changes: readonly EditorDocumentChange[];
  related?: boolean;
  canUndo: (change: EditorDocumentChange) => boolean;
  onUndo: (change: EditorDocumentChange) => void;
}) {
  const [expanded, setExpanded] = createSignal(false);
  const [page, setPage] = createSignal(0);
  const first = () => props.changes[0]!;
  const shown = () => props.changes.slice(page() * pageSize, (page() + 1) * pageSize);
  return (
    <details class="den-file-version__edits" onToggle={(event) => setExpanded(event.currentTarget.open)}>
      <summary data-tip={props.related ? changeAuthorLabel(first()) : undefined}>
        <span class="den-file-version__copy">
          <strong>
            {props.changes.length} {props.changes.length === 1 ? "edit" : "edits"}{props.related ? " from this turn" : " in the editor"}
          </strong>
          <Show when={!props.related}>
            <small data-testid="document-change-author">{changeAuthorLabel(first())}</small>
          </Show>
        </span>
        <Show when={!props.related}>
          <time class="den-file-version__when" dateTime={first().created_at}>{relativeTimeLabel(first().created_at)}</time>
        </Show>
        <span class="den-disclosure-caret" aria-hidden="true" />
      </summary>
      <Show when={expanded()}>
        <div class="den-file-version__edit-items">
          <For each={shown()}>{(change, index) => {
            const revision = () => props.changes.length - page() * pageSize - index();
            return (
              <div class="den-file-version__edit-item">
                <span class="den-file-version__copy">
                  <strong>{props.changes.length > 1 ? `Edit ${revision()}` : "Document edit"}</strong>
                  <small><time dateTime={change.created_at}>{new Date(change.created_at).toLocaleString()}</time></small>
                </span>
                <DenButton
                  variant="ghost"
                  compact
                  disabled={!props.canUndo(change)}
                  aria-label={change.reverted_by ? `Edit ${revision()} undone` : `Undo edit ${revision()}`}
                  onClick={() => props.onUndo(change)}
                >
                  {change.reverted_by ? "Undone" : "Undo"}
                </DenButton>
              </div>
            );
          }}</For>
          <TablePager
            compact
            class="den-file-version__edit-pager"
            ariaLabel="Edit pagination"
            page={page()}
            pageSize={pageSize}
            total={props.changes.length}
            onPageChange={setPage}
          />
        </div>
      </Show>
    </details>
  );
}
