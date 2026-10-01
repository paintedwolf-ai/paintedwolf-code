import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { loadComparisonSnapshot } from "../../api/source-reader.ts";
import { For, Show, batch, createEffect, createMemo, createSignal, on, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type {  SourceGitReview, SourceGitReviewFile, SourceWalkEffect } from "../../api/types.ts";
import { loadWalkGitReview } from "./walk-git-loading.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { writeClipboardText } from "../../utils/clipboard.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { DenSelect } from "../../components/primitives/DenSelect.tsx";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { gitChangeRefs, gitChangeTitle } from "../source/source-git-change.ts";
import type { GitReviewSelection } from "../documents/files-buffer-state.ts";
import { ReviewFileGroups } from "../review/ReviewFileGroups.tsx";
import { walkStepFiles, type WalkGitStep } from "./walk-model.ts";
import { observeSurfaceFailure, reportSurfaceFailure, type SurfaceFailureCopy } from "../../notices/surface-failure.ts";

const GIT_REVIEW_UNAVAILABLE: SurfaceFailureCopy = {
  code: "files_git_review_unavailable",
  title: "Git review unavailable",
  suggestedAction: "Reopen the commit review to try again.",
};
const GIT_REVIEW_PAGE_FAILED: SurfaceFailureCopy = {
  code: "files_git_review_page_failed",
  title: "More files unavailable",
  suggestedAction: "Choose Load more files to try again.",
};
const GIT_REVIEW_FILE_FAILED: SurfaceFailureCopy = {
  code: "files_git_review_file_failed",
  title: "File comparison unavailable",
  suggestedAction: "Select the file again to open it.",
};

export type OpenGitReviewFile = (step: WalkGitStep, review: SourceGitReview, file: SourceGitReviewFile, comparison: ComparisonSnapshot) => void;

type Props = {
  projectId: string;
  sessionId?: string;
  rootLabel?: string;
  client: LycaonClient | null;
  step: WalkGitStep;
  selection?: GitReviewSelection;
  onSelectionChange?: (selection: GitReviewSelection) => void;
  onOpenGitFile: OpenGitReviewFile;
  onOpenFile: (step: WalkGitStep, effect: SourceWalkEffect) => void;
};

function directory(path: string): string {
  const slash = path.lastIndexOf("/");
  return slash < 0 ? "" : path.slice(0, slash);
}

function failureOf(failure: unknown): unknown {
  return failure instanceof Error ? failure : "The Git review could not be loaded.";
}

export function WalkGitReview(props: Props) {
  const live = useResidentLive();
  const initial = untrack(() => props.selection);
  const [movement, setMovement] = createSignal(initial?.movement);
  const [parent, setParent] = createSignal(initial?.parent);
  const [loadingMore, setLoadingMore] = createSignal(false);
  const [opening, setOpening] = createSignal<string | null>(null);
  const [copyStatus, setCopyStatus] = createSignal("");
  let fileAbort: AbortController | undefined;

  createEffect(on(() => props.selection, (selection) => batch(() => {
    setMovement(selection?.movement);
    setParent(selection?.parent);
  })));

  const selectComparison = (selection: GitReviewSelection) => batch(() => {
    setMovement(selection.movement);
    setParent(selection.parent);
    props.onSelectionChange?.(selection);
  });
  const requestOptions = () => ({ movement: movement(), parent: parent(), sessionId: props.sessionId });
  const query = createSurfaceQuery({
    name: "walk-git-review",
    revalidateOnActivation: false,
    source: () => {
      const client = props.client;
      const projectId = props.projectId;
      const changeId = props.step.change.id;
      const opts = requestOptions();
      return client ? {
        client, projectId, changeId, opts,
        key: JSON.stringify([projectId, opts.sessionId ?? "", changeId, opts.movement ?? null, opts.parent ?? null]),
        scope: JSON.stringify([projectId, opts.sessionId ?? "", changeId]),
      } : null;
    },
    scope: (source) => source.scope,
    load: (source) => loadWalkGitReview(source.client, source.projectId, source.changeId, source.opts),
  });
  const review = query.value;
  observeSurfaceFailure(GIT_REVIEW_UNAVAILABLE, () => query.error(), () => props.projectId);
  // Without a review to show, the destination commit is the one way forward from a failed movement.
  const canReviewDestination = () => !!props.client && !!query.error() && !review()
    && (movement() !== false || parent() !== undefined);
  createEffect(on(() => [live(), props.projectId, props.client, props.step.change.id, movement(), parent()], () => {
    fileAbort?.abort();
    setLoadingMore(false); setOpening(null);
  }));
  onCleanup(() => fileAbort?.abort());

  const loadMore = async () => {
    const held = review();
    const capture = query.capture();
    const source = capture.source;
    if (!held?.next_cursor || !source || query.loading() || loadingMore()) return;
    setLoadingMore(true);
    try {
      const page = await loadWalkGitReview(source.client, source.projectId, source.changeId, {
        ...source.opts, cursor: held.next_cursor,
      });
      if (!capture.current()) return;
      if (page.before_commit !== held.before_commit || page.after_commit !== held.after_commit) {
        throw new Error("The Git review returned an inconsistent page. Reload the review.");
      }
      capture.publish({ ...page, files: [...held.files, ...page.files] });
    } catch (failure) { if (capture.current()) reportSurfaceFailure(GIT_REVIEW_PAGE_FAILED, failureOf(failure), source.projectId); }
    finally { if (capture.current()) setLoadingMore(false); }
  };

  const openFile = async (file: SourceGitReviewFile) => {
    const held = review();
    const source = query.displayed()?.source;
    if (!held || !source || opening() === file.path) return;
    fileAbort?.abort();
    const controller = new AbortController();
    fileAbort = controller;
    const step = props.step;
    setOpening(file.path);
    try {
      const comparison = await loadComparisonSnapshot(source.client, source.projectId,
        { kind: "git_change", change_id: source.changeId, path: file.path, movement: source.opts.movement, parent: source.opts.parent }, source.opts.sessionId, controller.signal);
      if (!controller.signal.aborted) props.onOpenGitFile(step, held, file, comparison);
    } catch (failure) { if (!controller.signal.aborted) reportSurfaceFailure(GIT_REVIEW_FILE_FAILED, failureOf(failure), source.projectId); }
    finally { if (!controller.signal.aborted) setOpening(null); }
  };

  type FileGroup = { path: string; files: SourceGitReviewFile[] };
  // Reuses unchanged groups so a source change keeps the rendered rows.
  const groups = createMemo<FileGroup[]>((previous) => {
    const byDirectory = new Map<string, SourceGitReviewFile[]>();
    for (const file of review()?.files ?? []) {
      const dir = directory(file.path);
      const files = byDirectory.get(dir) ?? [];
      files.push(file); byDirectory.set(dir, files);
    }
    return [...byDirectory].map(([path, files]) => {
      const prior = previous.find((group) => group.path === path);
      const same = prior && prior.files.length === files.length && prior.files.every((file, index) => file === files[index]);
      return same ? prior : { path, files };
    });
  }, []);
  const message = () => review()?.commit.message ?? props.step.change.detail ?? gitChangeTitle(props.step.change);
  const copy = async (value: string) => {
    try { await writeClipboardText(value); setCopyStatus("Commit ID copied."); }
    catch { setCopyStatus("The commit ID could not be copied."); }
  };
  const observed = createMemo(() => walkStepFiles(props.step));

  return <Scrollport class="den-files-info__body">
    <article class="den-walk-page__article" data-testid="walk-git-review">
      <header class="den-walk-page__head">
        <span class="den-walk-page__glyph" aria-hidden="true">⎇</span>
        <div class="den-walk-page__heading">
          <p class="den-walk-page__subtitle">{gitChangeTitle(props.step.change)}<Show when={gitChangeRefs(props.step.change)}> · {gitChangeRefs(props.step.change)}</Show><Show when={props.rootLabel}> · {props.rootLabel}</Show></p>
          <h1 class="den-walk-page__title" data-testid="walk-page-title">{message().split("\n")[0] || "Commit without a message"}</h1>
        </div>
      </header>
      <Show when={message().includes("\n")}>
        <details class="den-git-review__message"><summary><span class="den-disclosure-caret" aria-hidden="true" />Full commit message</summary><pre>{message()}</pre></details>
      </Show>
      <ShowLatest when={review()} by={(data) => data.change.id}>{(data) => <>
        <dl class="den-files-info__meta den-walk-page__meta" aria-label="Commit details">
          <div class="den-files-info__meta-row"><dt>Commit</dt><dd><button class="den-git-review__hash" type="button" aria-label="Copy commit ID" data-tip={data().commit.hash} onClick={() => void copy(data().commit.hash)}>{data().commit.hash.slice(0, 12)}</button></dd></div>
          <div class="den-files-info__meta-row"><dt>Author</dt><dd>{data().commit.author_name}</dd></div>
          <div class="den-files-info__meta-row"><dt>Committed</dt><dd><time dateTime={data().commit.committed_at} data-tip={new Date(data().commit.committed_at).toLocaleString()}>{relativeTimeLabel(data().commit.committed_at)}</time></dd></div>
          <Show when={data().commit.authored_at !== data().commit.committed_at}><div class="den-files-info__meta-row"><dt>Authored</dt><dd><time dateTime={data().commit.authored_at} data-tip={new Date(data().commit.authored_at).toLocaleString()}>{relativeTimeLabel(data().commit.authored_at)}</time></dd></div></Show>
        </dl>
        <div class="den-git-review__controls">
          <label>Compare <DenSelect
            aria-label="Git comparison"
            value={data().commit_comparison ? "commit" : "movement"}
            options={[
              { value: "commit", label: "Commit with parent" },
              { value: "movement", label: "Movement between tips", disabled: !props.step.change.from_commit },
            ]}
            onValueChange={(value) => selectComparison({ movement: value === "movement" })}
          /></label>
          <Show when={data().commit_comparison && data().commit.parents.length > 1}>
            <label>Parent <DenSelect
              aria-label="Comparison parent"
              value={String(parent() ?? 1)}
              options={data().commit.parents.map((hash, index) => ({ value: String(index + 1), label: `${index + 1} · ${hash.slice(0, 12)}` }))}
              onValueChange={(value) => selectComparison({ movement: false, parent: Number(value) })}
            /></label>
          </Show>
        </div>
        <p class="den-walk-page__note" data-testid="git-review-basis">{data().before_commit ? data().before_commit.slice(0, 12) : "Empty tree"} → {data().after_commit.slice(0, 12)}. {data().commit_comparison ? "Changes recorded in this commit." : "Changes between the recorded branch tips."} Files are scoped to this project folder.</p>
        <section class="den-walk-page__files den-git-review__files" aria-label="Git review files">
          <h2 class="den-walk-page__files-title" data-testid="git-review-count">{data().files_total} {data().files_total === 1 ? "file" : "files"} {data().commit_comparison ? "committed" : "changed"}<span class="den-git-review__stats"><span>+{data().insertions}</span><span>−{data().deletions}</span></span></h2>
          <Show when={data().files_total === 0}><p class="den-walk-page__empty">{data().commit_comparison ? "This commit contains no file changes in this folder." : "These commits contain the same files in this folder."}</p></Show>
          <ReviewFileGroups groups={groups()} rootLabel={props.rootLabel} openingPath={opening()}
            fileTestId="git-review-file" onOpen={file => void openFile(file)} />
          <Show when={!!data().next_cursor}><button type="button" class="den-git-review__button" disabled={loadingMore() || query.loading()} onClick={() => void loadMore()}>{loadingMore() ? "Loading…" : `Load more files (${data().files.length} of ${data().files_total})`}</button></Show>
        </section>
      </>}</ShowLatest>
      <Show when={query.showLoading()}><p role="status">Loading committed files…</p></Show>
      <Show when={canReviewDestination()}>
        <div class="den-git-review__controls">
          <button type="button" class="den-git-review__button" onClick={() => selectComparison({ movement: false })}>Review destination commit</button>
        </div>
      </Show>
      <span class="sr-only" role="status">{copyStatus()}</span>
      <Show when={observed().length > 0}>
        <details class="den-git-review__observed"><summary><span class="den-disclosure-caret" aria-hidden="true" />{observed().length} working-tree {observed().length === 1 ? "file changed" : "files changed"} when this movement was observed</summary>
          <p class="den-walk-page__note">These are the file changes recorded by the walk at observation time.</p>
          <ul class="den-walk-page__list"><For each={observed()}>{(effect) => <li><button type="button" class="den-walk-page__file" onClick={() => props.onOpenFile(props.step, effect)}>{effect.path}<span class="den-walk-page__open">Open observed version</span></button></li>}</For></ul>
        </details>
      </Show>
    </article>
  </Scrollport>;
}
