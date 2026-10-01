import { For, Show } from "solid-js";
import type { SearchHit } from "../../api/types.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
import { hitTitleSegments } from "../../search/search-highlight.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { SourcePathLink } from "../source/SourcePathLink.tsx";
import { SourceUrlLink } from "../source/SourceUrlLink.tsx";
import { SearchKindIcon } from "./SearchIcons.tsx";

type Props = {
  hit: SearchHit;
  selected: boolean;
  highlightTerms?: readonly string[];
  codeCaseSensitive?: boolean;
  gridTemplate: string;
  /** False when the fit shed the location column; defaults to shown. */
  showsLocation?: boolean;
  rootRefs?: readonly ResolveProjectRoot[];
  onSelect: () => void;
  onPivot: (field: string, value: string) => void;
  onNavigate: () => void;
};

export function SearchResultRow(props: Props) {
  const display = () => searchHitDisplay(props.hit);
  const projectId = () => props.hit.project_id?.trim() ?? "";
  const timeLabel = () => {
    const ts = props.hit.created_at;
    if (!ts) return undefined;
    const ms = Date.parse(ts);
    return Number.isFinite(ms) ? formatRelativeTime(ms) : undefined;
  };
  const openablePath = () => {
    const path = display().openPath;
    return path && projectId() ? path : undefined;
  };

  return (
    <article
      class="den-search-row den-list-row"
      classList={{ "den-search-row--selected": props.selected }}
      style={{ "--den-list-cols": props.gridTemplate }}
      data-testid="search-result-row"
      data-hit-kind={props.hit.hit_kind}
      onClick={() => props.onSelect()}
    >
      <span class="den-list-cell den-search-row__kind" data-column="kind">
        <span class="den-search-row__glyph">
          <SearchKindIcon kind={props.hit.hit_kind} />
        </span>
        <span class="den-search-row__kind-label">{display().kindLabel}</span>
      </span>

      <span class="den-list-cell den-search-row__title" data-column="title">
        <For each={hitTitleSegments(display().title, display().titleMatches, props.highlightTerms, {
          caseSensitive: props.hit.hit_kind === "code" && props.codeCaseSensitive,
        })}>
          {(segment) => (
            <Show when={segment.match} fallback={segment.text}>
              <mark class="den-search-mark">{segment.text}</mark>
            </Show>
          )}
        </For>
      </span>

      <Show when={props.showsLocation ?? true}>
      <span class="den-list-cell" data-column="location">
        <Show when={openablePath()} keyed>
          {(pathInfo) => (
            <SourcePathLink
              size="sm"
              truncate
              projectId={projectId()}
              path={pathInfo.path}
              line={pathInfo.line}
              label={display().context}
              rootRefs={props.rootRefs}
            />
          )}
        </Show>
        <Show when={!display().openPath ? display().openUrl : undefined} keyed>
          {(url) => (
            <SourceUrlLink
              class="den-search-row__secondary"
              url={url}
              label={display().context}
            />
          )}
        </Show>
        <Show
          when={!display().openPath && !display().openUrl && display().context}
        >
          {(ctx) => (
            <button
              type="button"
              class="den-search-row__secondary"
              data-testid="search-result-secondary"
              onClick={(e) => {
                e.stopPropagation();
                const path = display().pivotPath;
                if (path) {
                  props.onPivot("path", path);
                  return;
                }
                props.onNavigate();
              }}
            >
              {ctx()}
            </button>
          )}
        </Show>
      </span>
      </Show>

      <span class="den-list-cell den-list-cell--end" data-column="age">
        <Show when={timeLabel()}>
          {(label) => (
            <time class="den-search-row__time" datetime={props.hit.created_at}>
              {label()}
            </time>
          )}
        </Show>
      </span>
    </article>
  );
}
