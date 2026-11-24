import { modelIndependentWebE2e } from "./helpers.ts";
import { runWorktreeInspectionQualificationJourney } from "./worktree-inspection-journey.ts";

modelIndependentWebE2e(
  "two windows and two repos inspect sibling worktrees independently and resolve removal blockers",
  async ({ page, context, request }, testInfo) => {
    modelIndependentWebE2e.setTimeout(180_000);
    await runWorktreeInspectionQualificationJourney({ page, context, request, testInfo });
  },
);
