/** Per-test budget during parallel handoff checks. */
export const VITEST_DEFAULT_TIMEOUT_MS = 60_000;

/** Hooks need headroom for dynamic imports. */
export const VITEST_HOOK_TIMEOUT_MS = 60_000;

/** Testing Library polling for scheduler-dependent DOM updates. */
export const VITEST_ASYNC_TIMEOUT_MS = 30_000;

/** Suites that spawn repository subprocesses. */
export const VITEST_SUBPROCESS_SUITE_TIMEOUT_MS = 180_000;

/** Repository scans under instrumentation. */
export const VITEST_REPOSITORY_SCAN_TIMEOUT_MS = 180_000;
