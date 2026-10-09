export * from "./invariants/common.ts";
export * from "./invariants/nav-invariants.ts";
export * from "./invariants/focus-invariants.ts";
export * from "./invariants/cache-invariants.ts";
export * from "./invariants/placement-invariants.ts";
export * from "./invariants/split-invariants.ts";
export * from "./invariants/lifecycle-invariants.ts";

import type { InvariantEntry } from "./invariants/common.ts";
import { NAV_INVARIANTS } from "./invariants/nav-invariants.ts";
import { FOCUS_INVARIANTS } from "./invariants/focus-invariants.ts";
import { CACHE_INVARIANTS } from "./invariants/cache-invariants.ts";
import { BOOT_OPEN_INVARIANTS, VIEW_FILES_INVARIANTS } from "./invariants/lifecycle-invariants.ts";
import { PLACEMENT_INVARIANTS } from "./invariants/placement-invariants.ts";
import { SPLIT_INVARIANTS } from "./invariants/split-invariants.ts";

export const INVARIANT_CATALOG: InvariantEntry[] = [
  ...NAV_INVARIANTS,
  ...FOCUS_INVARIANTS,
  ...CACHE_INVARIANTS,
  ...BOOT_OPEN_INVARIANTS,
  ...PLACEMENT_INVARIANTS,
  ...SPLIT_INVARIANTS,
  ...VIEW_FILES_INVARIANTS,
];
