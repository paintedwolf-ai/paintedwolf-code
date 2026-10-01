function timeoutScale(): number {
  const value = process.env.PW_TEST_TIMEOUT_SCALE ?? "1";
  return /^[1-4]$/.test(value) ? Number.parseInt(value, 10) : 1;
}

function scaled(milliseconds: number): number {
  return milliseconds * timeoutScale();
}

/** Per-test budget during parallel handoff checks. */
export const VITEST_DEFAULT_TIMEOUT_MS = scaled(15_000);

/** Hooks need headroom for dynamic imports. */
export const VITEST_HOOK_TIMEOUT_MS = scaled(15_000);

/** Testing Library polling for scheduler-dependent DOM updates. */
export const VITEST_ASYNC_TIMEOUT_MS = scaled(5_000);

/** Suites that spawn repository subprocesses. */
export const VITEST_SUBPROCESS_SUITE_TIMEOUT_MS = scaled(60_000);

/** Repository scans under instrumentation. */
export const VITEST_REPOSITORY_SCAN_TIMEOUT_MS = scaled(60_000);
