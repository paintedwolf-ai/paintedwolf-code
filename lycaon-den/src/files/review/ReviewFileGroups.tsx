import { For, Show } from "solid-js";
import type { SourceChangeOp } from "../../api/types.ts";
import { fileBasename, opGlyph } from "./review-model.ts";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";

export type ReviewFile = {
  path: string;
  op: SourceChangeOp;
  before_path?: string;
  before_mode?: string;
  after_mode?: string;
  binary?: boolean;
  insertions?: number;
  deletions?: number;
};

export type ReviewFileGroup<T extends ReviewFile> = { path: string; label?: string; files: T[] };

export function ReviewFileGroups<T extends ReviewFile>(props: {
  groups: ReviewFileGroup<T>[];
  rootLabel?: string;
  openingPath?: string | null;
  fileTestId: string;
  onOpen: (file: T) => void;
}) {
  return <For each={props.groups}>{group => <div>
    <h3 class="den-git-review__directory">{group.label ?? (group.path || props.rootLabel || "Project root")}</h3>
    <ul class="den-walk-page__list"><For each={group.files}>{file => <li>
      <button type="button" class="den-walk-page__file" data-testid={props.fileTestId} data-path={file.path}
        aria-label={`Review ${file.path}`} aria-busy={props.openingPath === file.path} onClick={() => props.onOpen(file)}>
        <span class="den-walk-page__op" aria-hidden="true">{opGlyph(file.op)}</span>
        <span class="den-git-review__filename"><span data-file-change={wholeFileOpChange(file.op)}>{fileBasename(file.path)}</span><Show when={file.before_path && file.before_path !== file.path}><small>From {file.before_path}</small></Show></span>
        <span class="den-git-review__file-kind">{file.before_mode === "160000" || file.after_mode === "160000" ? "Submodule" : file.before_mode === "120000" || file.after_mode === "120000" ? "Symlink" : file.binary ? "Binary" : file.op === "create" ? "Added" : file.op === "delete" ? "Deleted" : file.op === "rename" ? "Renamed" : "Modified"}</span>
        <span class="den-git-review__stats"><Show when={!file.binary && file.insertions !== undefined && file.deletions !== undefined}><span>+{file.insertions}</span><span>−{file.deletions}</span></Show></span>
        <span class="den-walk-page__open">{props.openingPath === file.path ? "Opening…" : "Review diff"}</span>
      </button>
    </li>}</For></ul>
  </div>}</For>;
}
