import { webE2e } from "./helpers.ts";
import { runWorktreeInspectionQualificationJourney } from "./worktree-inspection-journey.ts";

webE2e(
  "desktop app inspects sibling worktrees across two windows and two repos without collateral removal",
  async ({ page, context, request }, testInfo) => {
    webE2e.setTimeout(180_000);
    await runWorktreeInspectionQualificationJourney({ page, context, request, testInfo });
  },
);
