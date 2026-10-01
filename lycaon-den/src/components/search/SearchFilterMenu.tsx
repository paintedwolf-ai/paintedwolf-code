import { For, Show } from "solid-js";
import type { SearchFacet } from "../../api/types.ts";
import {
  queryFilterActive,
  queryFilterOccurrences,
  setQueryFilter,
  toggleQueryFilter,
} from "../../search/search-query-model.ts";
import {
  clearSearchRefinements,
  hasSearchRefinements,
} from "../../search/search-filter-model.ts";
import { DenCheckbox } from "../primitives/DenCheckbox.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenRadio } from "../primitives/DenRadio.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";
import { formatSentenceCase } from "../../format/format-sentence-case.ts";

type Props = {
  facets: SearchFacet[];
  query: string;
  onQueryChange: (query: string) => void;
  include: string;
  exclude: string;
  onIncludeChange: (value: string) => void;
  onExcludeChange: (value: string) => void;
};

const FACET_ORDER = ["source", "trust"];

function sortFacets(facets: SearchFacet[]): SearchFacet[] {
  return facets
    .filter((facet) => {
      const key = facet.key.toLowerCase();
      if (key === "kind" || key === "verified") return false;
      // An empty facet would render a bare heading.
      return facet.values.length > 0;
    })
    .sort((a, b) => {
      const ai = FACET_ORDER.indexOf(a.key.toLowerCase());
      const bi = FACET_ORDER.indexOf(b.key.toLowerCase());
      const ar = ai === -1 ? 99 : ai;
      const br = bi === -1 ? 99 : bi;
      if (ar !== br) return ar - br;
      return a.key.localeCompare(b.key);
    });
}

export function SearchFilterMenu(props: Props) {
  const facets = () => sortFacets(props.facets);
  const verification = (): "any" | "true" | "false" => {
    const values = queryFilterOccurrences(props.query).filter(
      (occurrence) =>
        !occurrence.negated && occurrence.field === "verified",
    );
    if (values.length !== 1) return "any";
    const value = values[0]?.value.toLowerCase();
    return value === "true" || value === "false" ? value : "any";
  };
  const verificationCount = (value: "true" | "false") =>
    props.facets
      .find((facet) => facet.key.toLowerCase() === "verified")
      ?.values.find((entry) => entry.value.toLowerCase() === value)?.count;
  const setVerification = (value: "any" | "true" | "false") => {
    props.onQueryChange(
      setQueryFilter(
        props.query,
        "verified",
        value === "any" ? undefined : value,
      ),
    );
  };
  const hasFilters = () =>
    hasSearchRefinements(props.query, props.include, props.exclude);
  const clearFilters = () => {
    props.onQueryChange(clearSearchRefinements(props.query));
    props.onIncludeChange("");
    props.onExcludeChange("");
  };

  return (
    <Scrollport
      class="den-search-filter-menu"
      contentClass="den-search-filter-menu__content"
      data-testid="search-filter-menu"
      aria-label="Search filters"
    >
      <section class="den-search-filter-menu__section">
        <h3 class="den-search-filter-menu__heading" {...chromeProps()}>
          Verification
        </h3>
        <div
          class="den-search-filter-menu__list"
          role="radiogroup"
          aria-label="Verification"
        >
          <For
            each={[
              { value: "any", label: "Any status" },
              { value: "true", label: "Verified" },
              { value: "false", label: "Unverified" },
            ] as const}
          >
            {(option) => (
              <DenRadio
                class="den-search-filter-menu__row"
                name="search-verification"
                value={option.value}
                data-testid={`search-filter-verified-${option.value}`}
                checked={verification() === option.value}
                onChange={() => setVerification(option.value)}
              >
                <span class="den-search-filter-menu__label">{option.label}</span>
                <Show
                  when={
                    option.value !== "any"
                      ? verificationCount(option.value)
                      : undefined
                  }
                >
                  {(count) => (
                    <span class="den-search-filter-menu__count">{count()}</span>
                  )}
                </Show>
              </DenRadio>
            )}
          </For>
        </div>
      </section>

      <Show when={facets().length > 0}>
        <For each={facets()}>
          {(facet) => (
            <section class="den-search-filter-menu__section">
              <h3 class="den-search-filter-menu__heading" {...chromeProps()}>
                {formatSentenceCase(facet.key)}
              </h3>
              <ul class="den-search-filter-menu__list">
                <For each={facet.values}>
                  {(value) => {
                    const active = () =>
                      queryFilterActive(props.query, facet.key, value.value);
                    return (
                      <li>
                        <DenCheckbox
                          class="den-search-filter-menu__row"
                          data-testid={`search-facet-${facet.key}-${value.value}`}
                          checked={active()}
                          onChange={() =>
                            props.onQueryChange(
                              toggleQueryFilter(
                                props.query,
                                facet.key,
                                value.value,
                              ),
                            )
                          }
                        >
                          <span class="den-search-filter-menu__label">
                            {formatSentenceCase(value.value)}
                          </span>
                          <span class="den-search-filter-menu__count">
                            {value.count}
                          </span>
                        </DenCheckbox>
                      </li>
                    );
                  }}
                </For>
              </ul>
            </section>
          )}
        </For>
      </Show>

      <section class="den-search-filter-menu__section">
        <h3 class="den-search-filter-menu__heading" {...chromeProps()}>
          File patterns
        </h3>
        <div class="den-search-filter-menu__fields">
          <label class="den-search-filter-menu__field">
            <span>Include</span>
            <DenInput
              type="text"
              data-testid="search-include-glob"
              value={props.include}
              placeholder="**/*.ts"
              spellcheck={false}
              onInput={(event) =>
                props.onIncludeChange(event.currentTarget.value)
              }
            />
          </label>
          <label class="den-search-filter-menu__field">
            <span>Exclude</span>
            <DenInput
              type="text"
              data-testid="search-exclude-glob"
              value={props.exclude}
              placeholder="vendor/**"
              spellcheck={false}
              onInput={(event) =>
                props.onExcludeChange(event.currentTarget.value)
              }
            />
          </label>
        </div>
      </section>

      <Show when={hasFilters()}>
        <button
          type="button"
          class="den-search-filter-menu__clear"
          data-testid="search-filter-clear"
          onClick={clearFilters}
        >
          Clear filters
        </button>
      </Show>
    </Scrollport>
  );
}
