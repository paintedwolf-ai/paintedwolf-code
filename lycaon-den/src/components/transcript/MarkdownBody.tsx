import { Show, createMemo, createRenderEffect, createSignal, onCleanup } from "solid-js";
import type { Token } from "marked";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";
import {
  createMarkdownCodeFoldController,
  syncMarkdownFenceChrome,
} from "../../chat/markdown/code-fold.ts";
import { prepareMarkdownSource } from "../../chat/markdown/markdown-output.ts";
import { renderMarkdownHtml } from "../../chat/markdown/markdown-render.ts";
import { remoteImagePlaceholderURL } from "../../chat/markdown/untrusted-markdown.ts";
import {
  markdownProjectPathLinkContextMenu,
  onMarkdownProjectPathLinkClick,
} from "../../chat/markdown/markdown-project-path-link.ts";
import type {
  ProseCitationIndex,
  ProseNavigationIndex,
} from "../../chat/markdown/prose-path-opens.ts";
import { useTranscriptViewport } from "../../chat/stream/transcript-viewport.tsx";
import { useTranscriptDisclosureStore } from "../../chat/transcript/presentation/disclosure-state.tsx";
import { confirmAndOpenExternalLink } from "../../platform/desktop/external-link.ts";
import { clickSelectedText } from "../../platform/interaction/selection-gesture.ts";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../ContextMenu.tsx";

type Props = {
  /** Tokens retain references resolved by whole-document lexing. */
  source: string | Token[];
  class?: string;
  /** Compact labels accept inline formatting only. */
  inline?: boolean;
  /** Surface link destinations and disable project-path opens. */
  untrusted?: boolean;
  /** Project context for project-path links. */
  projectId?: string;
  onExternalLink?: (href: string) => void | Promise<unknown>;
  /** Cited-path index — citation layer for prose path opens. */
  citations?: ProseCitationIndex;
  /** Durable host-validated targets, independent of grounding. */
  navigation?: ProseNavigationIndex;
  /** Require host validation before assistant-authored project links activate. */
  requireValidatedProjectPaths?: boolean;
  /** Render HTML tokens as text and omit comments. */
  literalHtml?: boolean;
  /** Project roots for path context menus. */
  rootRefs?: readonly ResolveProjectRoot[];
  /** Session's active root; bare prose paths resolve against it. */
  activeRootId?: string;
  /** Transcript row scope for persisting code-fence fold state across remounts. */
  disclosureScope?: string;
};

/** Render sanitized finalized markdown. */
export function MarkdownBody(props: Props) {
  const chatDestination = useChatDestinationScope();
  const className = () =>
    ["markdown-body", props.class].filter(Boolean).join(" ");
  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    items: ContextMenuItem[];
  } | null>(null);

  const html = createMemo(() => {
    const source = props.source;
    const src = typeof source === "string" ? (props.inline ? source : prepareMarkdownSource(source)) : source;
    if (typeof src === "string" && !src.trim()) return "";
    return renderMarkdownHtml(src, {
      untrusted: props.untrusted === true,
      projectId: props.projectId,
      citations: props.citations,
      navigation: props.navigation,
      requireValidatedProjectPaths: props.requireValidatedProjectPaths,
      literalHtml: props.literalHtml,
      inline: props.inline,
    });
  });

  const onClick = (e: MouseEvent | KeyboardEvent) => {
    // Deferred images require explicit activation.
    const target = e.target;
    if (target instanceof Element) {
      const btn = target.closest(".den-md-remote-img");
      if (btn instanceof HTMLElement) {
        if (e instanceof KeyboardEvent) {
          if (e.key !== "Enter" && e.key !== " ") return;
          e.preventDefault();
        } else if (clickSelectedText(e, btn)) {
          return;
        }
        const url = remoteImagePlaceholderURL(btn);
        if (url) void confirmAndOpenExternalLink(url);
        return;
      }
    }
    if (props.onExternalLink && target instanceof Element) {
      const anchor = target.closest("a.den-external-link[href]");
      if (anchor instanceof HTMLAnchorElement) {
        if (e instanceof KeyboardEvent && e.key !== "Enter") return;
        e.preventDefault();
        e.stopPropagation();
        if (e instanceof MouseEvent && clickSelectedText(e, anchor)) return;
        void props.onExternalLink(anchor.getAttribute("href") ?? "");
        return;
      }
    }
    // Untrusted project paths remain inert.
    if (props.untrusted === true) return;
    if (onMarkdownProjectPathLinkClick(e, props.projectId, props.activeRootId)) return;
  };

  const onContextMenu = (e: MouseEvent) => {
    if (props.untrusted === true) return;
    const next = markdownProjectPathLinkContextMenu(e, {
      projectId: props.projectId,
      rootRefs: props.rootRefs,
      activeRootId: props.activeRootId,
      chatDestination: chatDestination(),
    });
    if (next) setMenu(next);
  };

  let rootRef: HTMLDivElement | undefined;
  const viewport = useTranscriptViewport();
  const disclosures = useTranscriptDisclosureStore() ?? viewport?.disclosures;
  const codeFold = createMarkdownCodeFoldController({
    viewport,
    disclosures,
    scope: () => props.disclosureScope,
  });
  onCleanup(() => codeFold.dispose());

  /* eslint-disable solid/no-innerhtml -- sanitized markdown HTML only */
  const bodyEl = (
    <div
      ref={rootRef}
      class={className()}
      classList={{ "markdown-body--untrusted": props.untrusted === true }}
      innerHTML={html()}
      onClick={onClick}
      onKeyDown={onClick}
      onAuxClick={onClick}
      onContextMenu={onContextMenu}
    />
  );
  /* eslint-enable solid/no-innerhtml */

  // Attach fence chrome synchronously so initial measurements read folded height.
  createRenderEffect(() => {
    if (props.inline) {
      codeFold.clear();
      return;
    }
    html();
    if (rootRef) syncMarkdownFenceChrome(rootRef, codeFold);
  });
  createRenderEffect(() => {
    if (!props.inline) codeFold.syncState();
  });

  return (
    <>
      {bodyEl}
      <Show when={menu()} keyed>
        {(state) => (
          <ContextMenu
            anchor={state.anchor}
            items={state.items}
            onDismiss={() => setMenu(null)}
          />
        )}
      </Show>
    </>
  );
}
