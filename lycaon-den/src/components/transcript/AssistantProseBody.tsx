import { Show, createMemo, createSignal, onCleanup, onMount } from "solid-js";
import type { ResolveProjectRoot } from "../../api/project-path.ts";
import type { RedactedSpan } from "../../api/types.ts";
import { useChatDestinationScope } from "../../chat/composer/chat-destination-scope.tsx";
import { prepareMarkdownSource } from "../../chat/markdown/markdown-output.ts";
import { renderMarkdownHtml } from "../../chat/markdown/markdown-render.ts";
import {
  paintProseRedaction,
  planProseRedaction,
} from "../../chat/markdown/prose-redaction.ts";
import { remoteImagePlaceholderURL } from "../../chat/markdown/untrusted-markdown.ts";
import { confirmAndOpenExternalLink } from "../../platform/desktop/external-link.ts";
import { clickSelectedText } from "../../platform/interaction/selection-gesture.ts";
import {
  markdownProjectPathLinkContextMenu,
  onMarkdownProjectPathLinkClick,
} from "../../chat/markdown/markdown-project-path-link.ts";
import type {
  ProseCitationIndex,
  ProseNavigationIndex,
} from "../../chat/markdown/prose-path-opens.ts";
import {
  ContextMenu,
  type ContextMenuAnchor,
  type ContextMenuItem,
} from "../ContextMenu.tsx";

import type { LycaonClient } from "../../api/client.ts";
import { createMessageNavigation } from "../../chat/markdown/message-navigation.ts";
import { createReferenceNavigation } from "../../chat/markdown/reference-navigation.ts";
import { NavigationChooser } from "./NavigationChooser.tsx";

type Props = {
  client?: LycaonClient | null;
  messageId?: string;
  source: string;
  /** Exact wire content, before display normalization. */
  messageContent?: string;
  literalHtml?: boolean;
  sessionId?: string | null;
  /** Project context for project-path navigation. */
  projectId?: string;
  /** Cited paths for prose navigation. */
  citations?: ProseCitationIndex;
  /** Host-authored navigation requests and resolutions, independent of grounding. */
  navigation?: ProseNavigationIndex;
  /** Project roots for path context menus. */
  rootRefs?: readonly ResolveProjectRoot[];
  /** Host-stamped offsets into the unchanged message content. */
  redactionSpans?: readonly RedactedSpan[];
};

/** Render accepted coordinator prose in one stable node. */
export function AssistantProseBody(props: Props) {
  let root: HTMLDivElement | undefined;
  const [visible, setVisible] = createSignal(typeof IntersectionObserver === "undefined");
  onMount(() => {
    if (!root || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(([entry]) => setVisible(entry?.isIntersecting ?? false), { rootMargin: "200px" });
    observer.observe(root);
    onCleanup(() => observer.disconnect());
  });
  const navigation = createMessageNavigation(props, visible);
  const referenceActions = createReferenceNavigation({
    scope: () => [props.source, props.messageContent, props.messageId, props.sessionId],
    navigation: navigation.navigation,
    resolve: navigation.resolve,
  });
  const [menu, setMenu] = createSignal<{
    anchor: ContextMenuAnchor;
    items: ContextMenuItem[];
  } | null>(null);
  const chatDestination = useChatDestinationScope();

  const onProseClick = (event: MouseEvent) => {
    const target = event.target;
    if (target instanceof Element) {
      const button = target.closest(".den-md-remote-img");
      if (button instanceof HTMLElement) {
        if (clickSelectedText(event, button)) return;
        const url = remoteImagePlaceholderURL(button);
        if (url) void confirmAndOpenExternalLink(url);
        return;
      }
    }
    if (target instanceof Element) {
      const button = target.closest("[data-den-navigation-reference]");
      const referenceId = button?.getAttribute("data-den-navigation-reference");
      if (button && referenceId) {
        event.preventDefault();
        event.stopPropagation();
        if (clickSelectedText(event, button)) return;
        void referenceActions.activate(button, referenceId, "open");
        return;
      }
    }
    onMarkdownProjectPathLinkClick(event, props.projectId);
  };

  // Render synchronously so the first transcript measurement includes prose.
  const html = createMemo(() => {
    // Replace spans before markdown shifts source offsets.
    const plan = planProseRedaction(props.source, props.redactionSpans ?? []);
    const src = prepareMarkdownSource(plan?.source ?? props.source);
    if (!src.trim()) return "";
    return paintProseRedaction(
      renderMarkdownHtml(src, {
        projectId: props.projectId,
        citations: props.citations,
        navigation: navigation.navigation(),
        requireValidatedProjectPaths: true,
        literalHtml: props.literalHtml,
      }),
      plan,
    );
  });

  /* eslint-disable solid/no-innerhtml -- Markdown is sanitized before redaction marks are added. */
  return (
    <>
      <div
        ref={root}
        class="markdown-body assistant-prose"
        innerHTML={html()}
        onClick={onProseClick}
        onContextMenu={(e) => {
          const next = markdownProjectPathLinkContextMenu(e, {
            projectId: props.projectId,
            rootRefs: props.rootRefs,
            chatDestination: chatDestination(),
            onReferenceAction: (anchor, referenceId, action) => void referenceActions.activate(anchor, referenceId, action),
          });
          if (next) setMenu(next);
        }}
      />
      <Show when={referenceActions.displayedChooser()} keyed>{(state) => <NavigationChooser
        action={state.action} anchor={state.anchor} reference={state.reference} error={state.error} busy={state.busy} roots={props.rootRefs}
        onDismiss={referenceActions.dismiss}
        onRetry={() => void referenceActions.activate(state.anchor, state.referenceId, state.action)}
        onOpen={(target) => void referenceActions.choose(target)}
      />}</Show>
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
  /* eslint-enable solid/no-innerhtml */
}
