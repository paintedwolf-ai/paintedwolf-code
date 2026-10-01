import { For, Show } from "solid-js";
import type {
  CitationGroundingCitedEvidenceView,
  CitationGroundingFindingView,
  CitationGroundingView,
} from "../../chat/grounding/citation-grounding-model.ts";
import {
  GROUNDING_ADVISORY_NOTE_SUFFIX,
  GROUNDING_CITATIONS_SECTION,
  groundingHostAddedNote,
  GROUNDING_PANEL_SCOPE,
  GROUNDING_PROVENANCE_MARK,
  GROUNDING_SOURCE_PREFIX,
} from "../../chat/grounding/grounding-copy.ts";
import {
  evidenceRecordDetailLines,
  evidenceShapeLabel,
  renderEvidenceRecordSummary,
  fidelityLabel,
  type EvidenceRecordView,
} from "../../chat/grounding/evidence-shape-model.ts";
import {
  exploreCitationRowQuery,
  exploreDslField,
} from "../../search/search-query-model.ts";
import { isExternalLinkHref } from "../../platform/desktop/external-link.ts";
import { SourcePathLink } from "../source/SourcePathLink.tsx";
import { SourceUrlLink } from "../source/SourceUrlLink.tsx";

export type CitationExploreContext = {
  sessionId?: string;
  legId?: string;
};

type CitationRowItem = CitationGroundingCitedEvidenceView | CitationGroundingFindingView;

function citationNote(item: CitationRowItem): string | undefined {
  return "note" in item ? item.note?.trim() || undefined : undefined;
}

function CitationRow(props: {
  item: CitationRowItem;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  onExplore?: (query: string) => void;
}) {
  const item = () => props.item;
  const repoPath = () => item().path?.trim() || undefined;
  const handle = () => item().handle?.trim() || undefined;
  const hasPathChrome = () => Boolean(repoPath() || handle());
  const exploreQuery = () => exploreCitationRowQuery(item());
  return (
    <li
      class="den-citation-grounding-citation"
      classList={{
        "den-citation-grounding-citation--matched": item().verdict === "matched",
        "den-citation-grounding-citation--traced": item().verdict === "traced",
        "den-citation-grounding-citation--unverifiable":
          item().verdict === "unverifiable",
      }}
      data-testid="citation-grounding-citation"
      data-verdict={item().verdict ?? ""}
      data-handle={handle() || undefined}
      data-path={repoPath() || undefined}
      data-line={
        item().line != null && Number.isFinite(item().line)
          ? String(item().line)
          : undefined
      }
      data-project-id={props.projectId?.trim() || undefined}
    >
      <div class="den-citation-grounding-citation-head">
        <Show when={item().verdictLabel}>
          {(label) => (
            <span
              class="den-citation-grounding-citation-verdict"
              data-testid="citation-grounding-verdict"
            >
              {label()}
            </span>
          )}
        </Show>
        <Show when={hasPathChrome()}>
          <SourcePathLink
            projectId={props.projectId ?? ""}
            path={repoPath()}
            line={item().line}
            handle={handle()}
            rootRefs={props.rootRefs}
            openable={item().openable}
          />
        </Show>
      </div>
      <Show when={item().excerpt}>
        {(excerpt) => (
          <p class="den-citation-grounding-citation-excerpt">{excerpt()}</p>
        )}
      </Show>
      <Show when={citationNote(item())}>
        {(note) => <p class="den-citation-grounding-citation-note">{note()}</p>}
      </Show>
      <Show when={props.onExplore && exploreQuery()}>
        {(query) => (
          <button
            type="button"
            class="den-citation-grounding-explore"
            data-testid="citation-grounding-row-explore"
            onClick={() => props.onExplore?.(query())}
          >
            Explore
          </button>
        )}
      </Show>
    </li>
  );
}

function EvidenceRecordSummary(props: {
  record: EvidenceRecordView;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
}) {
  const rec = () => props.record;
  const path = () => rec().path?.trim() || undefined;
  const urls = () => rec().urls;
  const shape = () => rec().shape;

  return (
    <Show
      when={path() && (shape() === "file_region" || shape() === "artifact")}
      fallback={
        <Show
          when={shape() === "url" && urls()[0]}
          keyed
          fallback={
            <p class="den-citation-grounding-record-summary">
              {renderEvidenceRecordSummary(rec())}
            </p>
          }
        >
          {(url) => (
            <p class="den-citation-grounding-record-summary">
              <SourceUrlLink url={url} />
              <Show when={urls().length > 1}>
                <span>{` +${urls().length - 1} more`}</span>
              </Show>
            </p>
          )}
        </Show>
      }
    >
      <p class="den-citation-grounding-record-summary">
        <SourcePathLink
          projectId={props.projectId ?? ""}
          path={path()}
          line={rec().line}
          handle={rec().handle}
          rootRefs={props.rootRefs}
        />
      </p>
    </Show>
  );
}

function EvidenceRecordRow(props: {
  record: EvidenceRecordView;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  onExplore?: (query: string) => void;
}) {
  const rec = () => props.record;
  const exploreQuery = () => exploreCitationRowQuery(rec());
  const detailLines = () => evidenceRecordDetailLines(rec());
  return (
    <li
      class="den-citation-grounding-record"
      data-testid="citation-grounding-record"
      data-shape={rec().shape ?? "unknown"}
      data-fidelity={rec().fidelity ?? ""}
      data-handle={rec().handle?.trim() || undefined}
      data-path={rec().path?.trim() || undefined}
      data-line={
        rec().line != null && Number.isFinite(rec().line)
          ? String(rec().line)
          : undefined
      }
      data-project-id={props.projectId?.trim() || undefined}
    >
      <div class="den-citation-grounding-record-head">
        <span class="den-citation-grounding-record-shape">
          {evidenceShapeLabel(rec().shape)}
        </span>
        <Show when={rec().fidelity}>
          {(tier) => (
            <span class="den-citation-grounding-record-trust">
              {fidelityLabel(tier())}
            </span>
          )}
        </Show>
      </div>
      <EvidenceRecordSummary
        record={rec()}
        projectId={props.projectId}
        rootRefs={props.rootRefs}
      />
      <Show when={detailLines().length > 0}>
        <ul class="den-citation-grounding-record-details">
          <For each={detailLines()}>
            {(line) => (
              <li>
                <Show
                  when={isExternalLinkHref(line)}
                  fallback={line}
                >
                  <SourceUrlLink url={line} />
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={props.onExplore && exploreQuery()}>
        {(query) => (
          <button
            type="button"
            class="den-citation-grounding-explore"
            data-testid="citation-grounding-record-explore"
            onClick={() => props.onExplore?.(query())}
          >
            Explore
          </button>
        )}
      </Show>
    </li>
  );
}

function CheckRow(props: {
  check: CitationGroundingView["checks"][number];
}) {
  return (
    <li
      class="den-citation-grounding-check"
      classList={{
        "den-citation-grounding-check--passed": props.check.status === "passed",
        "den-citation-grounding-check--failed": props.check.status === "failed",
        "den-citation-grounding-check--advisory": props.check.status === "advisory",
      }}
      data-testid="citation-grounding-check"
      data-check-id={props.check.id}
    >
      <div class="den-citation-grounding-check-head">
        <span class="den-citation-grounding-check-mark" aria-hidden="true">
          <Show when={props.check.status !== "failed"} fallback="✕">
            {GROUNDING_PROVENANCE_MARK}
          </Show>
        </span>
        <span class="den-citation-grounding-check-label">{props.check.label}</span>
      </div>
      <Show when={props.check.summary}>
        {(summary) => (
          <p class="den-citation-grounding-check-summary">{summary()}</p>
        )}
      </Show>
      <Show when={props.check.matched.length > 0}>
        <ul class="den-citation-grounding-tokens den-citation-grounding-tokens--matched">
          <For each={props.check.matched}>{(token) => <li>{token}</li>}</For>
        </ul>
      </Show>
      <Show when={props.check.failed.length > 0}>
        <ul
          class="den-citation-grounding-tokens"
          classList={{
            "den-citation-grounding-tokens--failed": props.check.status === "failed",
            "den-citation-grounding-tokens--review": props.check.status === "advisory",
          }}
        >
          <For each={props.check.failed}>{(token) => <li>{token}</li>}</For>
        </ul>
      </Show>
    </li>
  );
}

type Props = {
  view: CitationGroundingView;
  projectId?: string;
  rootRefs?: readonly import("../../api/project-path.ts").ResolveProjectRoot[];
  exploreContext?: CitationExploreContext;
  onExplore?: (query: string) => void;
};

export function CitationGroundingPanel(props: Props) {
  const view = () => props.view;
  const citationRows = () => [...view().citedEvidence, ...view().findings];
  const exploreContext = () => props.exploreContext;
  const sessionExploreQuery = () => {
    const sessionId = exploreContext()?.sessionId?.trim();
    return sessionId ? exploreDslField("session", sessionId) : undefined;
  };
  const legExploreQuery = () => {
    const legId = exploreContext()?.legId?.trim();
    return legId ? exploreDslField("leg", legId) : undefined;
  };
  return (
    <div
      class="den-citation-grounding"
      classList={{
        "den-citation-grounding--traced":
          (view().outcome === "traced" || view().hostAssembled) &&
          view().hadTraceableWork,
        "den-citation-grounding--partial":
          view().outcome === "partial" &&
          !view().hostAssembled &&
          view().hadTraceableWork,
      }}
      data-testid="citation-grounding-panel"
      data-outcome={view().outcome}
      data-host-assembled={view().hostAssembled ? "true" : undefined}
    >
      <p class="den-citation-grounding-headline">{view().headline}</p>
      <Show when={view().hadTraceableWork}>
        <p class="den-citation-grounding-scope" data-testid="citation-grounding-scope">
          {GROUNDING_PANEL_SCOPE}
        </p>
        <Show when={!view().hostAssembled}>
          <p
            class="den-citation-grounding-source"
            data-testid="citation-grounding-source"
          >
            {GROUNDING_SOURCE_PREFIX} {view().sourceLabel}
          </p>
        </Show>
        <Show when={view().hostAssembled}>
          <div
            class="den-citation-grounding-host-box"
            data-testid="citation-grounding-host-box"
          >
            <p
              class="den-citation-grounding-source"
              data-testid="citation-grounding-source"
            >
              {GROUNDING_SOURCE_PREFIX} {view().sourceLabel}
            </p>
            <p
              class="den-citation-grounding-source-note"
              data-testid="citation-grounding-source-note"
            >
              {groundingHostAddedNote(view().retryCount ?? 0)}
            </p>
          </div>
        </Show>
      </Show>
      <Show when={view().hintCode && !view().hostAssembled}>
        {(code) => <p class="den-citation-grounding-code">Code: {code()}</p>}
      </Show>
      <Show when={view().observedPathCount}>
        {(count) => (
          <p class="den-citation-grounding-context">
            {count()} path{count() === 1 ? "" : "s"} in leg evidence
            <Show when={view().observedPathsSample.length > 0}>
              {": "}
              {view().observedPathsSample.join(", ")}
            </Show>
          </p>
        )}
      </Show>
      <Show when={view().observedURLCount}>
        {(count) => (
          <p class="den-citation-grounding-context">
            {count()} URL{count() === 1 ? "" : "s"} in leg evidence
            <Show when={view().observedURLsSample.length > 0}>
              {": "}
              {view().observedURLsSample.join(", ")}
            </Show>
          </p>
        )}
      </Show>
      <Show when={view().citedURLs.length > 0}>
        <ul
          class="den-citation-grounding-tokens den-citation-grounding-tokens--matched"
          data-testid="citation-grounding-cited-urls"
        >
          <For each={view().citedURLs}>
            {(url) => (
              <li>
                <SourceUrlLink url={url} />
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={citationRows().length > 0}>
        <section
          class="den-citation-grounding-citations-section"
          data-testid="citation-grounding-citations-section"
        >
          <div class="den-citation-grounding-citations-head-row">
            <h4 class="den-citation-grounding-citations-head">
              {GROUNDING_CITATIONS_SECTION}
            </h4>
            <Show when={props.onExplore}>
              <div
                class="den-citation-grounding-citations-explore"
                data-testid="citation-grounding-section-explore"
              >
                <Show when={sessionExploreQuery()}>
                  {(query) => (
                    <button
                      type="button"
                      class="den-citation-grounding-explore"
                      data-testid="citation-grounding-session-explore"
                      onClick={() => props.onExplore?.(query())}
                    >
                      Explore session
                    </button>
                  )}
                </Show>
                <Show when={legExploreQuery()}>
                  {(query) => (
                    <button
                      type="button"
                      class="den-citation-grounding-explore"
                      data-testid="citation-grounding-leg-explore"
                      onClick={() => props.onExplore?.(query())}
                    >
                      Explore leg
                    </button>
                  )}
                </Show>
              </div>
            </Show>
          </div>
          <ul class="den-citation-grounding-citations">
            <For each={citationRows()}>
              {(item) => (
                <CitationRow
                  item={item}
                  projectId={props.projectId}
                  rootRefs={props.rootRefs}
                  onExplore={props.onExplore}
                />
              )}
            </For>
          </ul>
        </section>
      </Show>
      <Show when={view().proseLeakCount}>
        {(count) => (
          <p class="den-citation-grounding-context">
            {count()} citation{count() === 1 ? "" : "s"} duplicated in narrative
            <Show when={view().proseLeaksSample.length > 0}>
              {": "}
              {view().proseLeaksSample.join(", ")}
            </Show>
          </p>
        )}
      </Show>
      <Show when={view().proseAdvisoryCount}>
        {(count) => (
          <p
            class="den-citation-grounding-context den-citation-grounding-context--advisory"
            data-testid="citation-grounding-advisory"
          >
            {count()} lower-trust citation{count() === 1 ? "" : "s"} in narrative
            — {GROUNDING_ADVISORY_NOTE_SUFFIX}
            <Show when={view().proseAdvisoriesSample.length > 0}>
              {": "}
              {view().proseAdvisoriesSample.join(", ")}
            </Show>
          </p>
        )}
      </Show>
      <Show when={view().evidenceRecords.length > 0}>
        <ul class="den-citation-grounding-records">
          <For each={view().evidenceRecords}>
            {(record) => (
              <EvidenceRecordRow
                record={record}
                projectId={props.projectId}
                rootRefs={props.rootRefs}
                onExplore={props.onExplore}
              />
            )}
          </For>
        </ul>
      </Show>
      <Show when={view().checks.length > 0}>
        <ul class="den-citation-grounding-checks">
          <For each={view().checks}>{(check) => <CheckRow check={check} />}</For>
        </ul>
      </Show>
    </div>
  );
}
