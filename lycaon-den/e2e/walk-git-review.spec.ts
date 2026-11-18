import { modelIndependentWebE2e } from "./helpers.ts";
import { registerGitReviewJourney } from "./walk-git-review-journey.ts";

for (const browserName of ["chromium", "webkit"] as const) {
  modelIndependentWebE2e.describe(`walk Git review in ${browserName}`, () => {
    registerGitReviewJourney(browserName);
  });
}
