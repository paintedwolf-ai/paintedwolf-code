const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";

type MediaView = Pick<Window, "matchMedia">;

/** The MediaQueryList last resolved, and the matchMedia it came from. */
let memoizedSource: MediaView["matchMedia"] | undefined;
let memoizedQuery: MediaQueryList | null = null;

/** Caches the live media query per view. */
function reducedMotionQuery(view: MediaView | null | undefined): MediaQueryList | null {
  if (!view || typeof view.matchMedia !== "function") return null;
  if (view.matchMedia === memoizedSource) return memoizedQuery;
  let query: MediaQueryList | null = null;
  try {
    query = view.matchMedia(REDUCED_MOTION_QUERY);
  } catch {
    query = null;
  }
  memoizedSource = view.matchMedia;
  memoizedQuery = query;
  return query;
}

/** Reads the reduced-motion preference; pass a view to read another window's. */
export function prefersReducedMotion(
  view: MediaView | null | undefined = typeof window === "undefined"
    ? undefined
    : window,
): boolean {
  return reducedMotionQuery(view)?.matches ?? false;
}
