import { For, Show, createMemo } from "solid-js";
import type {
  FileBriefingLocation,
  FileBriefingSection,
} from "../../api/types.ts";
import { languageLabelForDetectedLanguage } from "../../components/source/editor/codemirror-lang.ts";
import { MarkdownBody } from "../../components/transcript/MarkdownBody.tsx";
import type { LiveFileBriefing } from "./file-briefing-live.ts";
import type { FileBriefingTarget } from "./file-briefing-target.ts";

type Props = {
  target: FileBriefingTarget;
  summary: LiveFileBriefing | null;
  requestFailed: boolean;
  contextLabel: string | null;
  loading: boolean;
  onRetry: () => void;
  onOpenLocation: (target: FileBriefingTarget, line?: number) => void;
};

const sectionLabel = {
  purpose: "Purpose",
  structure: "Structure",
  key_behavior: "Key behavior",
} as const;

export function fileSummaryFallbackMarkdown(summary: LiveFileBriefing): string {
  if (summary.sections.length > 0) return "";
  if (summary.status === "pending" || summary.status === "streaming") return "";
  return summary.fallback_text.trim() || summary.streamText?.trim() || "";
}

export type FileSummaryInlinePart = {
  kind: "text" | "code";
  text: string;
};

/** Briefing prose supports inline code spans only. */
export function splitFileSummaryInline(text: string): FileSummaryInlinePart[] {
  const parts: FileSummaryInlinePart[] = [];
  const codeSpan = /`([^`\n]+)`/g;
  let cursor = 0;
  for (const match of text.matchAll(codeSpan)) {
    const start = match.index;
    const end = start + match[0].length;
    if (text[start - 1] === "`" || text[end] === "`") continue;
    if (start > cursor) parts.push({ kind: "text", text: text.slice(cursor, start) });
    parts.push({ kind: "code", text: match[1] ?? "" });
    cursor = end;
  }
  if (cursor < text.length) parts.push({ kind: "text", text: text.slice(cursor) });
  return parts.length > 0 ? parts : [{ kind: "text", text }];
}

/** Only a unique, exact host-known declaration name is navigable. */
export function fileSummaryLocationsByName(
  locations: readonly FileBriefingLocation[],
): ReadonlyMap<string, FileBriefingLocation | null> {
  const byName = new Map<string, FileBriefingLocation | null>();
  for (const location of locations) {
    const name = location.name.trim();
    if (!name) continue;
    if (!byName.has(name)) {
      byName.set(name, location);
      continue;
    }
    const prior = byName.get(name);
    if (prior !== null && prior?.line !== location.line) byName.set(name, null);
  }
  return byName;
}

type SummarySectionProps = {
  section: FileBriefingSection;
  locations: ReadonlyMap<string, FileBriefingLocation | null>;
  onOpen: (line: number) => void;
};

function FileSummarySectionLine(props: SummarySectionProps) {
  const parts = createMemo(() => splitFileSummaryInline(props.section.text));
  return (
    <p class="den-file-summary__section">
      <strong>{sectionLabel[props.section.kind]}:</strong>{" "}
      <For each={parts()}>
        {(part) => {
          if (part.kind === "text") return part.text;
          const location = () => props.locations.get(part.text) ?? null;
          return (
            <Show when={location()} fallback={<code>{part.text}</code>}>
              {(target) => (
                <button
                  type="button"
                  class="den-file-summary__symbol"
                  data-tip={`${target().name} · Line ${target().line}`}
                  aria-label={`Open ${target().name} at line ${target().line}`}
                  onClick={() => props.onOpen(target().line)}
                >
                  <code>{part.text}</code>
                </button>
              )}
            </Show>
          );
        }}
      </For>
    </p>
  );
}

export function FileSummaryPanel(props: Props) {
  const fallbackMarkdown = createMemo(() =>
    props.summary ? fileSummaryFallbackMarkdown(props.summary) : "",
  );
  const locations = createMemo(() =>
    fileSummaryLocationsByName(props.summary?.locations ?? []),
  );
  const hasSections = () => (props.summary?.sections.length ?? 0) > 0;
  const hasExplanation = () => hasSections() || Boolean(fallbackMarkdown());
  const languageLabel = createMemo(() =>
    languageLabelForDetectedLanguage(
      props.summary?.preview.language ?? "",
      props.target.path,
    ),
  );
  const retryLabel = () => {
    if (props.target.presentation === "version") return "Summarize this version";
    if (props.target.presentation === "document") return "Summarize this draft";
    return "Summarize current file";
  };
  const summarizing = () =>
    props.loading ||
    props.summary?.status === "pending" ||
    props.summary?.status === "streaming";

  return (
    <section
      class="den-file-summary"
      data-testid="file-summary-content"
      aria-busy={summarizing() ? true : undefined}
    >
      <button
        type="button"
        class="den-file-summary__title"
        data-tip={props.target.path}
        data-tip-when-clipped
        onClick={() => props.onOpenLocation(props.target, 1)}
      >
        {props.target.path}
      </button>

      <Show when={props.contextLabel}>
        <div class="den-file-summary__context" data-testid="file-summary-context">
          {props.contextLabel}
        </div>
      </Show>

      <Show when={props.summary?.preview}>
        {(preview) => (
          <div class="den-file-summary__preview" data-testid="file-summary-preview">
            <button
              type="button"
              class="den-file-summary__meta"
              onClick={() => props.onOpenLocation(props.target, 1)}
            >
              <span>{languageLabel()}</span>
              <span aria-hidden="true">·</span>
              <span>
                {preview().line_count} {preview().line_count === 1 ? "line" : "lines"}
              </span>
            </button>
          </div>
        )}
      </Show>

      <Show when={hasExplanation()}>
        <Show
          when={hasSections()}
          fallback={
            <div class="den-file-summary__markdown" data-testid="file-summary-fallback">
              <MarkdownBody untrusted source={fallbackMarkdown()} />
            </div>
          }
        >
          <div class="den-file-summary__sections" data-testid="file-summary-sections">
            <For each={props.summary?.sections ?? []}>
              {(section) => (
                <FileSummarySectionLine
                  section={section}
                  locations={locations()}
                  onOpen={(line) => props.onOpenLocation(props.target, line)}
                />
              )}
            </For>
          </div>
        </Show>
      </Show>

      <Show
        when={
          !hasExplanation() &&
          summarizing()
        }
      >
        <p class="den-file-summary__state" role="status">
          Summarizing this file…
        </p>
      </Show>
      <Show
        when={
          !summarizing() &&
          (props.summary?.status === "failed" ||
            props.summary?.stale ||
            props.requestFailed ||
            (briefingSettledWithoutExplanation(props.summary) && !hasExplanation()))
        }
      >
        <Show when={props.summary?.stale}>
          <p class="den-file-summary__state">
            This summary does not match the current file revision.
          </p>
        </Show>
        <Show when={!props.summary?.stale && !hasExplanation()}>
          <p class="den-file-summary__state">Detailed summary is not available yet.</p>
        </Show>
        <button type="button" class="den-file-summary__action" onClick={props.onRetry}>
          {retryLabel()}
        </button>
      </Show>
      <Show when={!summarizing() && props.summary?.truncated}>
        <p class="den-file-summary__state">
          Summary shortened to fit the interactive budget.
        </p>
      </Show>
    </section>
  );
}

function briefingSettledWithoutExplanation(
  summary: LiveFileBriefing | null,
): summary is LiveFileBriefing {
  return summary?.status === "preview" || summary?.status === "complete";
}
