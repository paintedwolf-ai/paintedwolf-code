import { createEffect, createMemo, createSignal, For, onCleanup, Show } from "solid-js";
import type { SearchHit } from "../../api/types.ts";
import {
  longestMatchingRoot,
  resolveProjectFile,
  type ResolveProjectRoot,
} from "../../api/project-path.ts";
import { searchHitDisplay } from "../../search/search-hit-display.ts";
import { openSearchHit } from "../../search/search-hit-open.ts";
import { loadSearchToolContent, searchToolContentLabel } from "../../search/search-tool-content.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { hitTitleSegments } from "../../search/search-highlight.ts";
import { formatRelativeTime } from "../../time/time-copy.ts";
import { SourcePathLink } from "../source/SourcePathLink.tsx";
import { SourceUrlLink } from "../source/SourceUrlLink.tsx";
import { DetailPane } from "../list/DetailPane.tsx";
import { SearchKindIcon } from "./SearchIcons.tsx";
import { addToChat } from "../../chat/composer/add-to-chat.ts";
import { chatRefForSearchHit } from "../../chat/composer/search-add-to-chat.ts";
import { startChatAttachmentDrag } from "../../chat/composer/chat-attachment-drag.ts";

export type SearchDetailPivot = { label: string; field: string; value: string };

type Props = {
  hit: SearchHit;
  highlightTerms?: readonly string[];
  codeCaseSensitive?: boolean;
  pivots: SearchDetailPivot[];
  rootRefs?: readonly ResolveProjectRoot[];
  onPivot: (field: string, value: string) => void;
  onNavigate: () => void;
  onClose: () => void;
};

export function SearchHitDetail(props: Props) {
  const [openingTool, setOpeningTool] = createSignal(false);
  const [toolError, setToolError] = createSignal<string>();
  let toolRequest: AbortController | undefined;
  const hitKey = createMemo(() => props.hit.hit_id);
  createEffect(() => {
    hitKey();
    toolRequest?.abort();
    toolRequest = undefined;
    setOpeningTool(false);
    setToolError(undefined);
  });
  onCleanup(() => toolRequest?.abort());
  const openTool = async () => {
    if (openingTool()) return;
    const hit = props.hit;
    const request = new AbortController();
    toolRequest = request;
    setOpeningTool(true);
    setToolError(undefined);
    try {
      const client = getLycaonClient();
      if (!client) throw new Error("Connect to the host to open recorded tool content.");
      const document = await loadSearchToolContent(client, hit, request.signal);
      if (!request.signal.aborted) openFilesSurface({ kind: "chat-content", projectId: hit.project_id, document });
    } catch (error) {
      if (!request.signal.aborted) setToolError(error instanceof Error ? error.message : String(error));
    } finally {
      if (!request.signal.aborted) setOpeningTool(false);
    }
  };
  const display = () => searchHitDisplay(props.hit);
  const projectId = () => props.hit.project_id?.trim() ?? "";
  const timeLabel = () => {
    const ts = props.hit.created_at;
    if (!ts) return undefined;
    const ms = Date.parse(ts);
    return Number.isFinite(ms) ? formatRelativeTime(ms) : undefined;
  };
  const openableFile = () => {
    const path = display().openPath;
    return path && projectId() ? path : undefined;
  };

  const openFile = () => {
    const path = openableFile();
    if (!path) return;
    void openSourceLocation({
      intent: "permanent",
      projectId: projectId(),
      path: path.path,
      line: path.line,
      focus: true,
    });
  };

  const addRef = () =>
    chatRefForSearchHit(props.hit, props.rootRefs ?? []);

  const onAddToChat = () => {
    const ref = addRef();
    if (!ref) return;
    void addToChat(ref);
  };

  const provenanceAttrs = (): Record<string, string | undefined> => {
    const hit = props.hit;
    const pathInfo = openableFile();
    const refs = props.rootRefs ?? [];
    let rootId: string | undefined;
    if (pathInfo && refs.length > 0) {
      const resolved = resolveProjectFile({ roots: refs }, pathInfo.path);
      if (!("error" in resolved)) {
        rootId = longestMatchingRoot(resolved.absolutePath, refs)?.id;
      }
    }
    return {
      "data-project-id": projectId() || undefined,
      "data-session-id": hit.session_id?.trim() || undefined,
      "data-source-ref": hit.source_ref?.trim() || undefined,
      "data-hit-kind": (hit.hit_kind ?? "").trim() || undefined,
      "data-den-source-path": pathInfo?.path?.trim() || undefined,
      "data-den-source-line":
        pathInfo?.line != null ? String(pathInfo.line) : undefined,
      "data-root-id": rootId,
    };
  };

  return (
    <DetailPane
      class="den-search-detail"
      testId="search-hit-detail"
      provenanceAttrs={provenanceAttrs()}
      draggable={Boolean(addRef())}
      onDragStart={(event) => startChatAttachmentDrag(event, addRef())}
      eyebrow={
        <>
          <SearchKindIcon kind={props.hit.hit_kind} />
          <span>{display().kindLabel}</span>
        </>
      }
      meta={
        <Show when={timeLabel()}>
          {(label) => <time datetime={props.hit.created_at}>{label()}</time>}
        </Show>
      }
      onClose={() => props.onClose()}
    >
      <p class="den-search-detail__title">
        <For each={hitTitleSegments(display().title, display().titleMatches, props.highlightTerms, {
          caseSensitive: props.hit.hit_kind === "code" && props.codeCaseSensitive,
        })}>
          {(segment) => (
            <Show when={segment.match} fallback={segment.text}>
              <mark class="den-search-mark">{segment.text}</mark>
            </Show>
          )}
        </For>
      </p>

      <Show when={openableFile()} keyed>
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
      <Show when={!openableFile() ? display().openUrl : undefined} keyed>
        {(url) => (
          <SourceUrlLink
            class="den-search-detail__source"
            url={url}
            label={display().context}
          />
        )}
      </Show>

      <div class="den-search-detail__actions">
        <button
          type="button"
          class="den-search-backlink den-search-backlink--primary"
          data-testid="search-result-open"
          onClick={() => openSearchHit(props.hit, () => props.onNavigate())}
        >
          {display().openLabel}
        </button>
        <Show when={openableFile() && display().openLabel !== "Open"}>
          <button
            type="button"
            class="den-search-backlink"
            data-testid="search-detail-open-file"
            onClick={() => openFile()}
          >
            Open file
          </button>
        </Show>
        <Show when={addRef()}>
          <button
            type="button"
            class="den-search-backlink"
            data-testid="search-result-add-to-chat"
            onClick={() => onAddToChat()}
          >
            Add to chat
          </button>
        </Show>
      </div>

      <Show when={searchToolContentLabel(props.hit)}>
        {(label) => <button
          type="button"
          class="den-search-backlink self-start"
          data-testid="search-tool-content-open"
          disabled={openingTool()}
          onClick={() => void openTool()}
        >
          {openingTool() ? "Opening…" : `${label()} in Files`}
        </button>}
      </Show>
      <Show when={toolError()}>{error => <p role="alert">{error()}</p>}</Show>

      <Show when={display().preview}>
        {(preview) => (
          <pre
            class={`den-search-detail__preview den-search-detail__preview--${preview().format}`}
            data-testid="search-result-preview"
            data-format={preview().format}
          >
            {preview().text}
          </pre>
        )}
      </Show>

      <Show when={props.pivots.length > 0}>
        <div
          class="den-search-detail__pivots"
          role="group"
          aria-label="Refine from selection"
          data-testid="search-inspector"
        >
          <p class="den-search-detail__pivots-label">Refine</p>
          <For each={props.pivots}>
            {(pivot) => (
              <button
                type="button"
                class="den-search-backlink"
                data-testid={`search-pivot-${pivot.field}`}
                onClick={() => props.onPivot(pivot.field, pivot.value)}
              >
                {pivot.label}
              </button>
            )}
          </For>
        </div>
      </Show>
    </DetailPane>
  );
}
