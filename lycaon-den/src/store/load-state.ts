// Distinguishes empty, pending, and failed backend reads.

export type LoadState<T> =
  | { readonly state: "unloaded" }
  | { readonly state: "loading"; readonly previous?: T }
  | { readonly state: "error"; readonly message: string; readonly previous?: T }
  | { readonly state: "loaded"; readonly value: T };

export function unloaded<T>(): LoadState<T> {
  return { state: "unloaded" };
}

/** Carries rendered data through a reload. */
export function loading<T>(prior?: LoadState<T>): LoadState<T> {
  const previous = prior === undefined ? undefined : valueOf(prior);
  return previous === undefined ? { state: "loading" } : { state: "loading", previous };
}

/** Carries rendered data after a failed reload. */
export function loadFailed<T>(err: unknown, prior?: LoadState<T>): LoadState<T> {
  const previous = prior === undefined ? undefined : valueOf(prior);
  const message = err instanceof Error && err.message.trim() !== "" ? err.message : String(err);
  return previous === undefined
    ? { state: "error", message }
    : { state: "error", message, previous };
}

export function loaded<T>(value: T): LoadState<T> {
  return { state: "loaded", value };
}

/** Returns the current or retained value. */
export function valueOf<T>(ls: LoadState<T>): T | undefined {
  switch (ls.state) {
    case "loaded":
      return ls.value;
    case "loading":
    case "error":
      return ls.previous;
    case "unloaded":
      return undefined;
  }
}

/** True only for a successful, settled load. */
export function isLoaded<T>(ls: LoadState<T>): ls is { state: "loaded"; value: T } {
  return ls.state === "loaded";
}

/** Reports a successful or failed read. */
export function isResolved<T>(ls: LoadState<T>): boolean {
  return ls.state === "loaded" || ls.state === "error";
}

export function errorOf<T>(ls: LoadState<T>): string | undefined {
  return ls.state === "error" ? ls.message : undefined;
}

/** Reports an empty successful result. */
export function settledEmpty<T>(ls: LoadState<T>, isEmpty: (value: T) => boolean): boolean {
  return ls.state === "loaded" && isEmpty(ls.value);
}
