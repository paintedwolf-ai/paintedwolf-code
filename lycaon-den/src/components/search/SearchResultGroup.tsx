import { For, Show } from "solid-js";
import type { SearchHit } from "../../api/types.ts";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { ProjectHitGroup } from "../../search/search-query-model.ts";
import { SearchResultRow } from "./SearchResultRow.tsx";
import { ProjectFolderIcon } from "./SearchIcons.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";

type Props = {
  groups: ProjectHitGroup[];
  selectedHitKey: string | null;
  highlightTerms?: readonly string[];
  codeCaseSensitive?: boolean;
  issueNote?: string;
  busy?: boolean;
  gridTemplate: string;
  /** Narrow layouts can hide the location column. */
  showsLocation?: boolean;
  rootRefsForProject?: (projectId: string) => readonly ResolveProjectRoot[];
  onSelectHit: (hit: SearchHit, key: string) => void;
  onPivot: (field: string, value: string) => void;
  onNavigate: (hit: SearchHit) => void;
};

export function searchHitKey(hit: Pick<SearchHit, "hit_id">): string {
  return hit.hit_id;
}

function ProjectHeader(props: { group: ProjectHitGroup }) {
  return (
    <header
      class="den-search-group__header"
      classList={{
        "den-search-group__header--origin": props.group.isOrigin,
      }}
      data-testid={`search-group-${props.group.projectId}`}
      {...chromeProps()}
    >
      <span class="den-search-group__chip">
        <ProjectFolderIcon />
        <span class="den-search-group__name">{props.group.projectName}</span>
      </span>
      <span class="den-status-mark">{props.group.hits.length}</span>
    </header>
  );
}

export function SearchResultGroup(props: Props) {
  return (
    <div data-testid="search-result-groups">
      <Show when={props.issueNote}>
        {(note) => (
          <p class="den-search-issue" data-testid="search-issue-note" role="status">
            {note()}
          </p>
        )}
      </Show>
      <div role="list" aria-label="Search results" aria-busy={props.busy ?? false}>
        <For each={props.groups}>
          {(group) => (
            <section role="presentation">
              <ProjectHeader group={group} />
              <For each={group.hits}>
                {(hit) => {
                  const key = searchHitKey(hit);
                  return (
                    <div
                      data-hit-key={key}
                      role="listitem"
                      aria-current={props.selectedHitKey === key ? "true" : undefined}
                    >
                      <SearchResultRow
                        hit={hit}
                        selected={props.selectedHitKey === key}
                        highlightTerms={props.highlightTerms}
                        codeCaseSensitive={props.codeCaseSensitive}
                        gridTemplate={props.gridTemplate}
                        showsLocation={props.showsLocation ?? true}
                        rootRefs={props.rootRefsForProject?.(group.projectId)}
                        onSelect={() => props.onSelectHit(hit, key)}
                        onPivot={(field, pivotValue) =>
                          props.onPivot(field, pivotValue)
                        }
                        onNavigate={() => props.onNavigate(hit)}
                      />
                    </div>
                  );
                }}
              </For>
            </section>
          )}
        </For>
      </div>
    </div>
  );
}
