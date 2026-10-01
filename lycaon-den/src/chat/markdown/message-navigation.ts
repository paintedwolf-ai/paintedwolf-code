import { createMemo, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { NavigationReference } from "../../api/types.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { buildProseNavigationIndex, type ProseNavigationIndex } from "./prose-path-opens.ts";

export async function navigationContentHash(source: string): Promise<string> {
  const bytes = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(source));
  return [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

type Props = {
  client?: LycaonClient | null;
  sessionId?: string | null;
  messageId?: string;
  source: string;
  messageContent?: string;
  navigation?: ProseNavigationIndex;
};

export function createMessageNavigation(props: Props, active: () => boolean) {
  const content = () => props.messageContent ?? props.source;
  const scope = () => JSON.stringify([props.sessionId, props.messageId, content()]);
  const query = createSurfaceQuery({
    name: "message-navigation",
    required: false,
    source: () => {
      const { client, sessionId, messageId, navigation } = props;
      if (!active() || !client || !sessionId || !messageId || !navigation) return null;
      if (![...navigation.references.values()].some((ref) => ref.status === "pending")) return null;
      return { client, sessionId, messageId, content: content(), key: scope() };
    },
    load: async (source, signal) => {
      const hash = await navigationContentHash(source.content);
      signal.throwIfAborted();
      const response = await source.client.resolveMessageNavigation(source.sessionId, {
        message_id: source.messageId, content_sha256: hash,
      }, signal);
      if (response.content_sha256 !== hash) throw new Error("The message changed before its files were checked.");
      return response;
    },
  });
  const navigation = createMemo(() => {
    const response = query.value();
    return response ? buildProseNavigationIndex(response.references) : props.navigation;
  });
  let clickAbort: AbortController | undefined;
  onCleanup(() => clickAbort?.abort());
  const resolve = async (referenceId: string, candidateIndex?: number): Promise<NavigationReference | undefined> => {
    clickAbort?.abort();
    clickAbort = new AbortController();
    const abort = clickAbort;
    const { client, sessionId, messageId } = props;
    const source = content();
    const requestScope = scope();
    if (!client || !sessionId || !messageId) {
      const ref = untrack(navigation)?.references.get(referenceId);
      if (candidateIndex == null) return ref;
      const target = ref?.candidates?.[candidateIndex];
      return ref && target ? { ...ref, ...target, status: "resolved", candidates: undefined } : undefined;
    }
    const hash = await navigationContentHash(source);
    abort.signal.throwIfAborted();
    const response = await client.resolveMessageNavigation(sessionId, {
      message_id: messageId, content_sha256: hash, reference_id: referenceId, candidate_index: candidateIndex,
    }, abort.signal);
    if (abort.signal.aborted || requestScope !== scope() || response.content_sha256 !== hash) return undefined;
    return response.references.find((ref) => ref.id === referenceId);
  };
  return { navigation, error: query.error, resolve };
}
