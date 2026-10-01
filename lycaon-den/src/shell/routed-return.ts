import type { WorkspaceContext } from "../platform/windows/workspace-view-registry.ts";

/**
 * A routed visit's return target: the stage it opened and the navigation it
 * left. Nothing about the origin is copied. Its identity and label resolve
 * when the offer is read, so a chat switched in place or a column shown again
 * answers for itself.
 */
export type RoutedReturn<N extends string> = { stage: N; from: N };

/** What a navigation value presents in this window now. */
export type RoutedOrigin = { context: WorkspaceContext; label: string };

export type RoutedReturnOffer<N extends string> = { from: N; label: string };

/** Records a routed arrival; arriving where navigation already was records nothing. */
export function routedReturnFor<N extends string>(
  from: N,
  stage: N,
): RoutedReturn<N> | null {
  return from === stage ? null : { stage, from };
}

/**
 * The return `stage` offers, or null when the visit was direct, belongs to
 * another stage, or its origin is already visible in some workspace view.
 */
export function routedReturnOffer<N extends string>(
  record: RoutedReturn<N> | null,
  stage: N,
  resolve: {
    origin: (from: N) => RoutedOrigin | null;
    visible: (context: WorkspaceContext) => boolean;
  },
): RoutedReturnOffer<N> | null {
  if (!record || record.stage !== stage) return null;
  const origin = resolve.origin(record.from);
  if (!origin || resolve.visible(origin.context)) return null;
  return { from: record.from, label: `Back to ${origin.label}` };
}
