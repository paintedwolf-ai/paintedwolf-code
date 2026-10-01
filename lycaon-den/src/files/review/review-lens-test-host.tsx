import { onCleanup, type ComponentProps } from "solid-js";
import { ReviewLens } from "./ReviewLens.tsx";
import { resolveScope, setScopeResolver } from "../tree/scope-resolution.ts";

/**
 * Renders the Review lens with the resolver the files stage holds in the app:
 * the lens asks for answers, and this host supplies the selected chat.
 */
export function ReviewLensHost(props: ComponentProps<typeof ReviewLens>) {
  setScopeResolver(props.projectId, () => {
    const session = props.appStore.state.currentSession;
    const subject = session?.id ? { sessionId: session.id, title: session.title?.trim() ?? "", sessionScoped: true } : null;
    void resolveScope(props.projectId, props.client, subject);
  });
  onCleanup(() => setScopeResolver(props.projectId, null));
  return <ReviewLens {...props} />;
}
