import { expect, type Page } from "@playwright/test";
import {
  bootstrapChatSession,
  openProgressTab,
  webE2e,
} from "./helpers.ts";
import {
  coordinationBoardFixture,
  coordinationCoordinatorContextFixture,
  coordinationProgressFixture,
  coordinationFindingsFixture,
  coordinationSseStream,
  coordinationWorkersFixture,
} from "./coordination-fixtures.ts";

async function installCoordinationUiRoutes(page: Page) {
  let sessionId = "";

  // Project activation uses the real API-created project.
  await page.route("**/v1/sessions", async (route) => {
    const request = route.request();
    if (request.method() === "POST") {
      const response = await route.fetch();
      const body = (await response.json()) as { id?: string };
      if (body.id) sessionId = body.id;
      await route.fulfill({ response });
      return;
    }
    await route.continue();
  });

  await page.route("**/v1/sessions/*/progress", async (route) => {
    await route.fulfill({ json: coordinationProgressFixture });
  });

  await page.route("**/v1/sessions/*/findings", async (route) => {
    await route.fulfill({ json: coordinationFindingsFixture });
  });

  await page.route("**/v1/sessions/*/coordinator-context", async (route) => {
    await route.fulfill({ json: coordinationCoordinatorContextFixture });
  });

  await page.route("**/v1/projects/*/board*", async (route) => {
    await route.fulfill({ json: coordinationBoardFixture });
  });

  await page.route("**/v1/workers**", async (route) => {
    // The route glob also matches worker cancellation requests.
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    const workers = coordinationWorkersFixture(sessionId || "sess-pending");
    await route.fulfill({ json: { workers } });
  });

  await page.route(/\/v1\/events/, async (route) => {
    const projectId = new URL(route.request().url()).searchParams.get("project_id");
    await route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      headers: { "Cache-Control": "no-cache" },
      body: projectId ? coordinationSseStream(projectId) : ": connected\n\n",
    });
  });
}

async function expectCoordinationSurfaces(page: Page) {
  await openProgressTab(page);
  await expect(page.getByTestId("progress-strip")).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByTestId("progress-batch-phase")).toHaveText(
    "Combining results",
  );
  await expect(
    page.getByTestId("progress-strip-item").filter({ hasText: "Ship auth" }),
  ).toBeVisible();
  await expect(page.getByTestId("progress-strip-columns").locator(":scope > .den-scrollport__viewport")).toHaveAttribute(
    "data-overlayscrollbars-viewport",
    /scrollbarHidden/,
  );
}

webE2e.describe("coordination UI surfaces", () => {
  webE2e.beforeEach(async ({ page }) => {
    await installCoordinationUiRoutes(page);
    await bootstrapChatSession(page);
    await expectCoordinationSurfaces(page);
  });

  webE2e("progress rail shows phase chip and coordinator plan rows", async ({
    page,
  }) => {
    await expect(
      page.getByTestId("progress-strip-item").filter({ hasText: "Ship auth" }),
    ).toBeVisible();
  });

  webE2e("worklog renders findings and reservations", async ({
    page,
  }) => {
    await page.getByTestId("worklog-open").click();
    const panel = page.getByTestId("worklog-panel");
    await expect(panel).toBeVisible();
    await expect(page.getByTestId("worklog-finding-row")).toHaveCount(2);
    await expect(page.getByTestId("worklog-reservation-group")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("worklog-reservation-path").first()).toHaveText(
      "pkg/a.go",
    );
    await page.getByTestId("worklog-panel").getByLabel("Close worklog").click();
  });
});
