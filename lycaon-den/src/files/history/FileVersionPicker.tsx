import { For, Show, createMemo, createSignal, type JSX } from "solid-js";
import type { SourceFileVersion } from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { createPresentationWaiting } from "../../ui/presentation.ts";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { bindingForHandler } from "../../shortcuts/display-binding-for.ts";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { revealElementInScrollport } from "../../platform/scrolling/scrollport-motion.ts";
import {
  buildFileHistoryRows,
  fileCommitMeta,
  gitLaneSettled,
  shortCommit,
  type FileHistoryCommit,
  type FileHistoryRow,
} from "./file-history-model.ts";
import {
  fileVersionActor,
  fileVersionBranchLabel,
  fileVersionLandingLabel,
  isFileVersionRestoreCause,
  fileVersionTitle,
  type FileVersionHistory,
  type FileVersionView,
} from "./file-version.ts";
import { gitChangeAction, gitChangeRefs } from "../source/source-git-change.ts";

type Props = {
  editHistory?: (dismiss: () => void) => JSX.Element;
  selected: FileVersionView | null;
  /** The working file is gone; Current presents its last deletion. */
  currentAbsent?: boolean;
  history: FileVersionHistory;
  onCurrent: () => void;
  onSelect: (version: SourceFileVersion) => void;
  onSelectCommit: (entry: FileHistoryCommit) => void;
  onRestoreVersion: (version: SourceFileVersion) => void;
  onRestoreCommit: (entry: FileHistoryCommit) => void;
  /** A null reason enables restore. */
  restoreReason: (
    row:
      | { kind: "version"; version: SourceFileVersion }
      | { kind: "commit"; entry: FileHistoryCommit },
  ) => string | null;
  onOpen: () => void;
  onLoadMore: () => void;
};

function selectedLabel(selected: FileVersionView | null): string {
  if (!selected) return "Current";
  return selected.reviewedThroughOrdinal ? "Reviewed changes" : relativeTimeLabel(selected.ts);
}

/** Uses an absolute timestamp at the observed-history boundary. */
function trackedSinceLabel(ts: string): string {
  const when = new Date(ts);
  if (Number.isNaN(when.getTime())) return "Tracked from here";
  return `Tracked since ${
    when.toLocaleDateString(undefined, { month: "short", day: "numeric" })
  }, ${when.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" })}`;
}

function versionGlyphVariant(
  row: Extract<FileHistoryRow, { kind: "version" }>,
): "ai" | "you" | "muted" {
  switch (row.version.origin) {
    case "agent":
      return "ai";
    case "user":
      return "you";
    default:
      return "muted";
  }
}

export function FileVersionPicker(props: Props) {
  const [open, setOpen] = createSignal(false);
  const waiting = createPresentationWaiting(() => open() && (props.history.status === "loading" || props.history.gitStatus === "loading"));
  let triggerEl: HTMLButtonElement | undefined;

  const dismiss = () => setOpen(false);
  const history = createMemo(() => buildFileHistoryRows(props.history));
  const rows = () => history().rows;
  const currentCommit = () => history().currentCommits[0];

  const handleSurfaceKeyDown = (event: KeyboardEvent) => {
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const items = Array.from(
        (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>(
          ".den-file-version__option:not([disabled])",
        ),
      );
      if (!items.length) return;
      const active = document.activeElement as HTMLElement | null;
      const currentIndex = active ? items.indexOf(active) : -1;
      let nextIndex = 0;
      if (event.key === "ArrowDown") {
        nextIndex = currentIndex + 1 < items.length ? currentIndex + 1 : 0;
      } else {
        nextIndex = currentIndex - 1 >= 0 ? currentIndex - 1 : items.length - 1;
      }
      items[nextIndex]?.focus();
    } else if (event.key === "Home") {
      event.preventDefault();
      const items = (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>(
        ".den-file-version__option:not([disabled])",
      );
      items[0]?.focus();
    } else if (event.key === "End") {
      event.preventDefault();
      const items = (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>(
        ".den-file-version__option:not([disabled])",
      );
      items[items.length - 1]?.focus();
    }
  };

  const versionRow = (
    row: Extract<FileHistoryRow, { kind: "version" }>,
  ) => {
    const version = row.version;
    const selected = () => !props.selected?.reviewedThroughOrdinal && props.selected?.versionId === version.id;
    const reason = props.restoreReason({ kind: "version", version });
    const meta = [
      fileVersionBranchLabel(version),
      version.git_change ? gitChangeRefs(version.git_change) || "Git" : fileVersionActor(version),
      fileVersionLandingLabel(version),
    ].filter(Boolean).join(" · ");
    const note = row.commits[0];
    const commitRelation = isFileVersionRestoreCause(version.cause)
      ? "matches commit"
      : "committed";
    const activate = () => {
      dismiss();
      props.onSelect(version);
    };
    return (
      <div
        class="den-file-version__option den-file-version__option--tl"
        classList={{ "den-file-version__option--selected": selected() }}
        role="menuitemradio"
        tabIndex={0}
        aria-checked={selected()}
        onKeyDown={(event) => {
          if (event.key !== "Enter" && event.key !== " ") return;
          event.preventDefault();
          activate();
        }}
        ref={(element) => {
          // Long histories open at the selected version.
          if (!selected()) return;
          queueMicrotask(() =>
            revealElementInScrollport(element, { block: "center" }),
          );
        }}
        onClick={activate}
      >
        <span class="den-file-version__rail" aria-hidden="true">
          <Show
            when={selected()}
            fallback={
              <Show
                when={version.git_change && row.commits.length === 0}
                fallback={
                  <span
                    class="den-file-version__glyph"
                    classList={{
                      "den-file-version__glyph--ai": versionGlyphVariant(row) === "ai",
                      "den-file-version__glyph--you": versionGlyphVariant(row) === "you",
                      "den-file-version__glyph--muted": versionGlyphVariant(row) === "muted",
                      "den-file-version__glyph--ringed": row.commits.length > 0,
                    }}
                  />
                }
              >
                <ThemeIcon slot="git" size={12} />
              </Show>
            }
          >
            <span class="den-file-version__check">✓</span>
          </Show>
        </span>
        <span class="den-file-version__copy">
          <strong>{fileVersionTitle(version)}</strong>
          <small>
            {meta}
            <Show when={note}>
              {(commit) => (
                <>
                  {meta ? ` · ${commitRelation} ` : `${commitRelation} `}
                  <span class="den-file-version__hash">
                    {shortCommit(commit().commit)}
                  </span>
                  <Show when={commit().subject.trim()}>
                    {(subject) => <> {subject()}</>}
                  </Show>
                  <Show when={row.commits.length > 1}>
                    {" "}+{row.commits.length - 1}
                  </Show>
                </>
              )}
            </Show>
          </small>
        </span>
        <span class="den-file-version__when">
          {relativeTimeLabel(version.created_at)}
        </span>
        <Show when={!reason}>
          <button
            type="button"
            class="den-file-version__restore"
            data-testid="file-version-restore-row"
            aria-label={`Restore this version of the file`}
            onClick={(event) => {
              event.stopPropagation();
              dismiss();
              props.onRestoreVersion(version);
            }}
          >
            Restore
          </button>
        </Show>
      </div>
    );
  };

  const commitRow = (entry: FileHistoryCommit, nested: boolean) => {
    const commit = entry.commit;
    const selected = () => props.selected?.commit?.commit === commit.commit;
    const reason = props.restoreReason({ kind: "commit", entry });
    const activate = () => {
      dismiss();
      props.onSelectCommit(entry);
    };
    return (
      <div
        class="den-file-version__option den-file-version__option--tl"
        classList={{
          "den-file-version__option--selected": selected(),
          "den-file-version__option--nested": nested,
        }}
        role="menuitemradio"
        tabIndex={0}
        aria-checked={selected()}
        data-testid="file-version-commit"
        onKeyDown={(event) => {
          if (event.key !== "Enter" && event.key !== " ") return;
          event.preventDefault();
          activate();
        }}
        onClick={activate}
      >
        <span class="den-file-version__rail" aria-hidden="true">
          <Show
            when={selected()}
            fallback={<span class="den-file-version__glyph den-file-version__glyph--commit" />}
          >
            <span class="den-file-version__check">✓</span>
          </Show>
        </span>
        <span class="den-file-version__copy">
          <strong>Committed</strong>
          <small>
            <span class="den-file-version__hash">{shortCommit(commit.commit)}</span>
            {" "}
            {fileCommitMeta(commit)}
          </small>
        </span>
        <span class="den-file-version__when">
          {relativeTimeLabel(commit.committed_at)}
        </span>
        <Show when={!reason}>
          <button
            type="button"
            class="den-file-version__restore"
            data-testid="file-version-restore-row"
            aria-label={`Restore the file as commit ${shortCommit(commit.commit)} holds it`}
            onClick={(event) => {
              event.stopPropagation();
              dismiss();
              props.onRestoreCommit(entry);
            }}
          >
            Restore
          </button>
        </Show>
      </div>
    );
  };

  const historyRow = (row: FileHistoryRow) => {
    switch (row.kind) {
      case "version":
        return versionRow(row);
      case "commit":
        return commitRow(row.entry, false);
      case "arrival":
        return (
          <>
            <div class="den-file-version__arrival" data-testid="file-version-arrival">
              <span class="den-file-version__rail" aria-hidden="true">
                <ThemeIcon slot="arrow-down" size={12} />
              </span>
              <small>
                Arrived with {gitChangeAction(row.change)}
                <Show when={gitChangeRefs(row.change)}>
                  {(refs) => <> · {refs()}</>}
                </Show>
              </small>
              <span class="den-file-version__when">
                {relativeTimeLabel(row.change.observed_at)}
              </span>
            </div>
            <For each={row.commits}>{(entry) => commitRow(entry, true)}</For>
          </>
        );
      case "boundary":
        return (
          <div
            class="den-file-version__boundary"
            role="separator"
            data-testid="file-version-boundary"
          >
            <span>{trackedSinceLabel(row.trackedSince)}</span>
          </div>
        );
    }
  };

  return (
    <div class="den-file-version">
      <button
        type="button"
        class="den-file-version__trigger"
        classList={{
          "den-file-version__trigger--historical": props.selected != null,
        }}
        data-testid="file-version-trigger"
        data-tip={
          bindingForHandler("files.openVersionHistory")
            ? `Version history (${bindingForHandler("files.openVersionHistory")})`
            : "Version history"
        }
        data-tip-pos="below"
        aria-label={`File version: ${selectedLabel(props.selected)}`}
        aria-haspopup="menu"
        aria-expanded={open()}
        ref={(element) => {
          triggerEl = element;
        }}
        onClick={() => {
          const next = !open();
          setOpen(next);
          if (next) props.onOpen();
        }}
      >
        <ThemeIcon slot="rewind" size={13} />
        <span>{selectedLabel(props.selected)}</span>
        <ThemeIcon slot="chevron-down" size={11} />
      </button>
      <Show when={open()}>
        <AnchoredSurface
          class="den-file-version__menu"
          role="menu"
          ariaLabel="Version history"
          anchor={() => triggerEl}
          preferredSide="bottom"
          align="end"
          onDismiss={dismiss}
          onKeyDown={handleSurfaceKeyDown}
        >
          {props.editHistory?.(dismiss)}
          <div class="den-file-version__head">
            <div class="den-file-version__heading">Version history</div>
            <button
              type="button"
              class="den-file-version__option"
              classList={{
                "den-file-version__option--selected": props.selected == null,
              }}
              data-testid="file-version-current"
              role="menuitemradio"
              aria-checked={props.selected == null}
              onClick={() => {
                dismiss();
                props.onCurrent();
              }}
            >
              <span class="den-file-version__check" aria-hidden="true">
                {props.selected == null ? "✓" : ""}
              </span>
              <span class="den-file-version__copy">
                <strong>Current</strong>
                <small>
                  {props.currentAbsent ? "Deleted · no working file" : "Editable working file"}
                  <Show when={!props.currentAbsent && currentCommit()}>
                    {(commit) => (
                      <>
                        {" · committed "}
                        <span class="den-file-version__hash">
                          {shortCommit(commit().commit)}
                        </span>
                        <Show when={commit().subject.trim()}>
                          {(subject) => <> {subject()}</>}
                        </Show>
                      </>
                    )}
                  </Show>
                </small>
              </span>
            </button>
            <div class="den-file-version__separator" role="separator" />
          </div>
          <Scrollport class="den-file-version__scroll">
            <Show when={waiting() && props.history.status === "loading"}>
              <div class="den-file-version__empty" role="status">
                Loading history…
              </div>
            </Show>
            {/* Empty history requires a definitive Git result. */}
            <Show
              when={
                props.history.status === "ready" &&
                props.history.gitStatus === "ready" &&
                gitLaneSettled(props.history.gitHistoryState) &&
                rows().length === 0
              }
            >
              <div class="den-file-version__empty">No earlier versions</div>
            </Show>
            <For each={rows()}>{historyRow}</For>
            <Show
              when={
                props.history.status === "ready" &&
                props.history.gitStatus === "loading" && waiting()
              }
            >
              <div
                class="den-file-version__git-loading"
                role="status"
                aria-label="Loading Git history"
              >
                <span class="den-file-version__spinner" aria-hidden="true" />
                <span>Loading Git history…</span>
              </div>
            </Show>
            <Show
              when={
                props.history.status === "ready" &&
                props.history.gitStatus !== "loading" &&
                (props.history.nextVersionsCursor !== null ||
                  props.history.nextGitCursor !== null)
              }
            >
              <button
                type="button"
                class="den-file-version__option"
                disabled={props.history.loadingMore}
                onClick={() => props.onLoadMore()}
              >
                <span class="den-file-version__check" aria-hidden="true" />
                <span class="den-file-version__copy">
                  <strong>
                    {props.history.loadingMore
                      ? "Loading…"
                      : "Load earlier history"}
                  </strong>
                </span>
              </button>
            </Show>
          </Scrollport>
        </AnchoredSurface>
      </Show>
    </div>
  );
}
