import path from "node:path";
import { writeFileSync } from "node:fs";
import { expect } from "@playwright/test";
import {
  bootstrapActiveProject,
  e2eTempDir,
  e2eUniqueLabel,
  webE2e,
} from "./helpers.ts";

const PAGE_SIZE = 100;
const RESULT_COUNT = 260;

function indexedFixture(root: string): void {
  const lines = Array.from(
    { length: RESULT_COUNT },
    (_, index) => `paginationneedle result ${index + 1}`,
  );
  writeFileSync(path.join(root, "results.txt"), lines.join("\n"));
}

webE2e.describe("stage search pagination", () => {
  webE2e("pages the initial Stage search without editing its query", async ({
    page,
    request,
  }) => {
    const root = e2eTempDir(request, "search-pagination");
    indexedFixture(root);
    await bootstrapActiveProject(
      page,
      request,
      root,
      e2eUniqueLabel("Search pagination"),
    );

    await page.getByTestId("project-search-entry").click();
    await expect(page.getByTestId("global-search-view")).toBeVisible();
    await expect(page.getByTestId("search-query-input")).toHaveValue("kind:code");

    const footer = page.getByTestId("search-results-footer");
    await expect(footer).toContainText(
      `1–${PAGE_SIZE} of ${RESULT_COUNT} results`,
      { timeout: 30_000 },
    );

    const results = page.getByTestId("search-results-list").locator(":scope > .den-scrollport__viewport");
    await expect(results).toHaveAttribute(
      "data-overlayscrollbars-viewport",
      /scrollbarHidden/,
    );
    await expect(page.getByTestId("search-result-row")).toHaveCount(PAGE_SIZE);

    const nextPage = page.getByTestId("search-page-next");
    await nextPage.click();
    await expect(footer).toContainText(`101–200 of ${RESULT_COUNT} results`);
    const rows = page.getByTestId("search-result-row");
    await expect(rows).toHaveCount(PAGE_SIZE);
    await expect(rows.first()).toContainText("result 101");
    await expect(rows.last()).toContainText("result 200");

    await nextPage.click();
    await expect(footer).toContainText(
      `201–${RESULT_COUNT} of ${RESULT_COUNT} results`,
    );
    await expect(page.getByTestId("search-result-row")).toHaveCount(
      RESULT_COUNT - PAGE_SIZE * 2,
    );
    await expect(nextPage).toHaveCount(0);

    await page.getByTestId("search-page-previous").click();
    await expect(footer).toContainText(`101–200 of ${RESULT_COUNT} results`);
    await expect(rows.first()).toContainText("result 101");
  });
});
