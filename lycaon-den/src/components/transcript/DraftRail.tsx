import { For, Show, createEffect, createMemo, createSignal, onCleanup } from "solid-js";
import type { DraftVersion } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { loadDraftVersions } from "../../chat/draft/draft-versions-load.ts";
import {
  coordinatorDraftPriorCount,
  draftSummaryLine,
} from "../../chat/transcript/projection/draft-model.ts";
import { generatingTokensLabel } from "../../chat/transcript/projection/generating-tokens.ts";
import { useTranscriptEntry } from "../../chat/transcript/presentation/transcript-entry.ts";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { createTablistKeyboard } from "../../platform/interaction/roving-focus.ts";

export function DraftRail(props: {
  sessionId?: string | null;
  slotId: string;
  body?: string;
  versionCount: number;
  live: boolean;
  generatingTokens?: number;
  client?: LycaonClient | null;
}) {
  const viewport = useTranscriptViewport();
  const disclosureKey = () => transcriptDisclosureKey.draft(props.slotId);
  const [expanded, setExpanded] = createSignal(false);
  const [versions, setVersions] = createSignal<DraftVersion[] | null>(null);
  const [loading, setLoading] = createSignal(false);
  const [selected, setSelected] = createSignal<number | null>(null);
  const { bindTranscriptEntry } = useTranscriptEntry(() => ({
    sessionId: props.sessionId ?? undefined,
    entryKey: props.slotId,
  }));
  let versionsTablist: HTMLDivElement | undefined;
  createTablistKeyboard(() => versionsTablist);

  const priorCount = () => coordinatorDraftPriorCount({ draft_version_count: props.versionCount });
  const variantA = () => priorCount() === 0;
  const bodyText = () => props.body?.trim() ?? "";

  createEffect(() => {
    if (!expanded() || variantA()) return;
    const sid = props.sessionId?.trim();
    const slot = props.slotId?.trim();
    const client = props.client;
    if (!sid || !slot || !client) return;

    let cancelled = false;
    setLoading(true);
    void loadDraftVersions(client, sid, slot, priorCount())
      .then((rows) => {
        if (cancelled) return;
        setVersions(rows);
        const sorted = [...rows].sort((a, b) => a.version_index - b.version_index);
        setSelected((cur) => cur ?? Math.max(0, sorted.length - 1));
      })
      .catch(() => {
        if (!cancelled) setVersions([]);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    onCleanup(() => {
      cancelled = true;
    });
  });

  const sortedVersions = createMemo(() => {
    const rows = versions();
    if (!rows) return [];
    return [...rows].sort((a, b) => a.version_index - b.version_index);
  });

  const selectedVersion = (): DraftVersion | undefined => {
    const rows = sortedVersions();
    const idx = selected();
    return idx != null ? rows[idx] : undefined;
  };

  const collapsedPreview = () => draftSummaryLine(bodyText());

  const closedLabel = () =>
    variantA()
      ? collapsedPreview()
      : `Draft versions (${priorCount()})`;

  // Single drafts expand only when the preview omits text.
  const hasExpandableBody = () => {
    const body = bodyText();
    if (!body) return false;
    if (body.includes("\n")) return true;
    return collapsedPreview() !== body;
  };

  const toggleLabel = () => {
    if (variantA()) return expanded() ? "Hide" : "Show";
    return expanded() ? "Hide versions" : `Versions (${priorCount()})`;
  };

  const expandable = () => {
    if (variantA()) return !props.live && hasExpandableBody();
    return priorCount() > 0;
  };

  const expandedBody = () => {
    const selected = selectedVersion();
    if (!variantA() && selected) return selected.body;
    return props.body ?? "";
  };

  const toggleExpanded = () => {
    const next = !expanded();
    const finishMotion = viewport?.beginDisclosureMotion(disclosureKey(), next ? "open" : "close");
    setExpanded(next);
    finishMotion?.();
  };

  return (
    <aside
      ref={(element) => bindTranscriptEntry(element)}
      class="den-draft-rail"
      classList={{ "den-draft-rail--live": props.live }}
      data-testid="draft-rail"
      data-draft-slot-id={props.slotId}
      data-draft-live={props.live ? "true" : "false"}
      data-draft-version-count={String(props.versionCount)}
      data-draft-variant={variantA() ? "a" : "b"}
      data-disclosure-key={disclosureKey()}
    >
      <div class="den-draft-rail-head">
        <span class="den-draft-rail-label">{props.live ? "Drafting…" : "Drafts"}</span>
        <Show when={props.live ? generatingTokensLabel(props.generatingTokens) : null}>
          {(label) => (
            <span class="den-draft-rail-generating" data-testid="draft-rail-generating">
              {label()}
            </span>
          )}
        </Show>
        <Show when={expandable()}>
          <button
            type="button"
            class="den-draft-rail-toggle"
            data-testid="draft-rail-toggle"
            aria-expanded={expanded()}
            onClick={() => toggleExpanded()}
          >
            {toggleLabel()}
          </button>
        </Show>
      </div>

      <Show when={expanded()}>
        <Show when={!variantA()}>
          <div
            ref={versionsTablist}
            class="den-draft-rail-tabs"
            role="tablist"
            aria-label="Draft versions"
            data-testid="draft-rail-tabs"
          >
            <For each={sortedVersions()}>
              {(version, index) => (
                <button
                  type="button"
                  role="tab"
                  class="den-draft-rail-tab"
                  classList={{ "den-draft-rail-tab--active": index() === selected() }}
                  aria-selected={index() === selected()}
                  data-testid={`draft-rail-tab-v${version.version_index}`}
                  onClick={() => setSelected(index())}
                >
                  <span class="den-draft-rail-tab-index">v{version.version_index}</span>
                  <span class="den-draft-rail-tab-badge">
                    {version.outcome_label?.trim() || "Superseded"}
                  </span>
                </button>
              )}
            </For>
          </div>
          <Show when={loading()}>
            <span class="den-draft-rail-loading" data-testid="draft-rail-loading">
              Loading…
            </span>
          </Show>
        </Show>
        <div class="den-draft-rail-body" data-testid="draft-rail-expanded-body">
          <span class="den-draft-rail-plain">{expandedBody()}</span>
        </div>
      </Show>

      <Show when={!expanded()}>
        <div
          class="den-draft-rail-body den-draft-rail-body--collapsed"
          data-testid={props.live ? "draft-rail-live-body" : "draft-rail-summary-body"}
        >
          <Show
            when={props.live}
            fallback={
              <span class="den-draft-rail-summary">{closedLabel()}</span>
            }
          >
            <span class="den-draft-rail-live-prose">{props.body ?? ""}</span>
          </Show>
        </div>
      </Show>
    </aside>
  );
}
