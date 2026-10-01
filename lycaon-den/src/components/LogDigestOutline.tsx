import { For, Show } from "solid-js";
import type { ReadLogDigestView } from "../chat/tool/read-tool-output.ts";
import {
  formatLogFormatLabel,
  formatTimeSpan,
} from "../chat/tool/read-tool-output.ts";
import type { ResolveProjectRoot } from "../api/project-path.ts";
import { copyTextToClipboard } from "../utils/clipboard.ts";
import { SourcePathLink } from "./source/SourcePathLink.tsx";
import { formatSentenceCase } from "../format/format-sentence-case.ts";

type Props = {
  view: ReadLogDigestView;
  projectId?: string;
  /** Roots with wire ids — enables Add to chat on line anchors. */
  rootRefs?: readonly ResolveProjectRoot[];
};

async function copyLineAnchor(path: string, line: number) {
  const text = path ? `${path}:${line}` : `line ${line}`;
  await copyTextToClipboard(text);
}

export function LogDigestOutline(props: Props) {
  const digest = () => props.view.digest;
  const projectId = () => props.projectId?.trim() ?? "";
  const sourcePath = () => props.view.path.trim();
  const canOpenSource = () => Boolean(projectId() && sourcePath());

  return (
    <div class="den-tool-part-card-log-digest" data-testid="log-digest-outline">
      <header class="den-tool-part-card-log-digest-head">
        <span class="den-tool-part-card-log-digest-format">
          {formatLogFormatLabel(digest().format)}
        </span>
        <Show when={digest().time_span}>
          {(span) => (
            <span class="den-tool-part-card-log-digest-span">
              {formatTimeSpan(span().start_at, span().end_at)}
            </span>
          )}
        </Show>
        <span class="den-tool-part-card-log-digest-records">
          {digest().parsed_count}/{digest().record_count} records
          <Show when={props.view.totalLines > 0}>
            {" "}
            · {props.view.totalLines} lines
          </Show>
        </span>
        <Show when={digest().truncated}>
          <span class="den-tool-part-card-badge den-tool-part-card-badge--stale">
            Sampled
          </span>
        </Show>
      </header>

      <Show when={(digest().facets?.length ?? 0) > 0}>
        <div class="den-tool-part-card-log-digest-facets">
          <For each={digest().facets}>
            {(facet) => (
              <div class="den-tool-part-card-log-digest-facet">
                <span class="den-tool-part-card-log-digest-facet-key">
                  {formatSentenceCase(facet.key)}
                </span>
                <For each={facet.values}>
                  {(entry) => (
                    <span class="den-tool-part-card-badge">
                      {entry.value} ({entry.count})
                    </span>
                  )}
                </For>
              </div>
            )}
          </For>
        </div>
      </Show>

      <Show when={(digest().clusters?.length ?? 0) > 0}>
        <ol class="den-tool-part-card-log-digest-clusters">
          <For each={digest().clusters}>
            {(cluster) => (
              <li>
                <Show
                  when={canOpenSource() ? sourcePath() : undefined}
                  fallback={
                    <button
                      type="button"
                      class="den-source-path-plain den-source-path--chip"
                      data-testid="log-digest-line-anchor"
                      data-tip="Copy line anchor for read ranges"
                      onClick={() =>
                        void copyLineAnchor(
                          props.view.path,
                          cluster.first_line,
                        )
                      }
                    >
                      L{cluster.first_line}
                    </button>
                  }
                  keyed
                >
                  {(path) => (
                    <SourcePathLink
                      chip
                      projectId={projectId()}
                      path={path}
                      line={cluster.first_line}
                      label={`L${cluster.first_line}`}
                      rootRefs={props.rootRefs}
                    />
                  )}
                </Show>
                <span class="den-tool-part-card-log-digest-cluster-body">
                  <Show when={cluster.severity}>
                    {(severity) => (
                      <span class="den-tool-part-card-badge">
                        {formatSentenceCase(severity().toLowerCase())}
                      </span>
                    )}
                  </Show>
                  <span class="den-tool-part-card-log-digest-template">
                    {cluster.template}
                  </span>
                  <span class="den-tool-part-card-log-digest-count">
                    ×{cluster.count}
                  </span>
                </span>
              </li>
            )}
          </For>
        </ol>
      </Show>

      <Show when={props.view.truncationBanner}>
        <p class="den-tool-part-card-note">{props.view.truncationBanner}</p>
      </Show>
    </div>
  );
}
