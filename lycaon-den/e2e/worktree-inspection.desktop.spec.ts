import { test } from "@playwright/test";
import { runWorktreeInspectionQualificationJourney } from "./worktree-inspection-journey.ts";

test(
  "desktop app inspects sibling worktrees across two windows and two repos without collateral removal",
  async ({ page, context, request }, testInfo) => {
    test.setTimeout(180_000);
    await runWorktreeInspectionQualificationJourney({ page, context, request, testInfo });
  },
);
