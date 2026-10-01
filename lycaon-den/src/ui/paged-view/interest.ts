/** Cancels one wait without cancelling work shared with another presenter. */
export function withInterest<T>(work: Promise<T>, ...signals: (AbortSignal | undefined)[]): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const finish = () => { for (const signal of signals) signal?.removeEventListener("abort", abort); };
    const abort = () => { finish(); reject(new DOMException("The presentation request was canceled.", "AbortError")); };
    work.then(value => { finish(); resolve(value); }, (error: unknown) => {
      finish();
      reject(error instanceof Error ? error : new Error("The presentation request failed.", { cause: error }));
    });
    for (const signal of signals) signal?.addEventListener("abort", abort, { once: true });
    if (signals.some(signal => signal?.aborted)) abort();
  });
}

export function waitForRetry(signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); reject(new DOMException("The command was canceled.", "AbortError")); };
    const timer = setTimeout(() => { signal.removeEventListener("abort", abort); resolve(); }, 250);
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
}
