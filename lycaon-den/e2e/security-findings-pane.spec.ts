import { expect, type Page } from "@playwright/test";
import type { FindingLedgerResponse, SecurityFinding } from "../src/api/types.ts";
import {
  bootstrapActiveProject,
  bootstrapChatSession,
  CONTEXT_NAV_WITH,
  liveChatStage,
  waitForChatComposerReady,
  webE2e,
} from "./helpers.ts";

const scanId = "scan-e2e-1";

const scanSummary = {
  id: scanId,
  categories: ["sast"],
  scanner_id: "opengrep-sast",
  status: "complete",
  findings_count: 1,
  findings_stored: 1,
  findings_by_level: { high: 1 },
  scan_scope: "repo",
  created_at: "2026-01-02T00:00:00Z",
};

const scanFindings: SecurityFinding[] = [
  {
    rule_id: "opengrep:sql",
    level: "high",
    message: "SQL concat",
    locations: [{ uri: "src/main.go", start_line: 12 }],
    fingerprints: { primary: "fp-e2e" },
    tool: { driver_id: "opengrep", name: "OpenGrep" },
    properties: { lycaon: { hint_code: "SCAN_SQL" } },
  },
];

const ledgerCounts = {
  open: 1,
  reopened: 0,
  fixed: 0,
  not_observed: 0,
  unverified: 0,
  ignored: 0,
};

async function selectRunsView(page: Page) {
  const overflow = page.getByTestId("scans-overflow-trigger");
  await overflow.click();
  await page.getByTestId("ledger-open-runs").click();
  await expect(page.getByTestId("scans-run-picker")).toBeVisible();
}

async function installScanRoutes(page: Page, findings = scanFindings, partial = false) {
  await page.route("**/v1/projects/*/findings/query", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    await route.fulfill({
      json: {
        project_id: "proj-e2e",
        entries: findings.map((finding) => ({
          finding,
          state: "open",
          scanner_id: "opengrep-sast",
          first_seen_at: "2026-01-01T00:00:00Z",
          last_seen_at: "2026-01-02T00:00:00Z",
          observations: 1,
          last_scan_id: scanId,
        })),
        counts: { ...ledgerCounts, open: findings.length },
        by_level: { critical: 0, high: findings.length, medium: 0, low: 0, info: 0, unknown: 0 },
        total_match: findings.length,
      } satisfies FindingLedgerResponse,
    });
  });

  const summary = {
    ...scanSummary,
    findings_count: findings.length,
    findings_stored: findings.length,
    findings_by_level: { high: findings.length },
    ...(partial ? {
      coverage_status: "partial",
      warning_summary: [{ kind: "file_partial_semantics", count: 14, files: 3, rules: 0 }],
    } : {}),
  };

  await page.route("**/v1/projects/*/scans?*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    await route.fulfill({
      json: { scans: [summary] },
    });
  });

  await page.route(`**/v1/projects/*/scans/${scanId}*`, async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    if (pathname.endsWith("/query") || pathname.endsWith("/sarif")) {
      await route.fallback();
      return;
    }
    const full = new URL(route.request().url()).searchParams.get("view") === "full";
    await route.fulfill({ json: { ...summary, ...(full && partial ? {
      warnings: Array.from({ length: 14 }, (_, index) => ({
        kind: "file_partial_semantics", file: `src/pipeline-${index % 3}.py`,
        start_line: 73 + index, start_column: 18, construct: "unsupported_ast_to_il:Slices",
      })),
    } : {}) } });
  });

  await page.route(`**/v1/projects/*/scans/${scanId}/query`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    await route.fulfill({
      json: {
        scan_id: scanId,
        findings,
        guidance: [],
        total_match: findings.length,
      },
    });
  });

  await page.route(`**/v1/projects/*/scans/${scanId}/sarif`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/sarif+json",
      headers: {
        "Content-Disposition": `attachment; filename="scan-${scanId}.sarif.json"`,
      },
      body: JSON.stringify({ version: "2.1.0", runs: [] }),
    });
  });
}

webE2e.describe("Security findings pane", () => {
  webE2e("loads results only after queued and running scans complete", async ({ page, request }) => {
    await installScanRoutes(page);
    let status = "pending";
    const queryStatuses: string[] = [];
    const summary = () => ({ ...scanSummary, status, long_running: false,
      findings_count: status === "complete" ? 1 : 0,
      findings_stored: status === "complete" ? 1 : 0,
      progress: status === "running" ? { chunks: 3, completed: 1, files: 20 } : undefined,
    });
    await page.route("**/v1/projects/*/scans?*", (route) => route.fulfill({ json: { scans: [summary()] } }));
    await page.route(`**/v1/projects/*/scans/${scanId}?*`, (route) => route.fulfill({ json: summary() }));
    await page.route(`**/v1/projects/*/scans/${scanId}/query`, async (route) => {
      queryStatuses.push(status);
      if (status !== "complete") {
        await route.fulfill({ status: 409, json: { code: "scan_not_complete", message: "Results are not ready" } });
        return;
      }
      await route.fallback();
    });
    await bootstrapActiveProject(page, request, undefined, undefined, { contextNav: CONTEXT_NAV_WITH("security") });
    await page.getByTestId("project-security-entry").click();
    await selectRunsView(page);
    await expect(page.getByTestId("scans-empty")).toContainText("Scan queued");
    expect(queryStatuses).toEqual([]);
    await expect(page.getByTestId("scans-counts")).toHaveCount(0);
    status = "running";
    await expect(page.getByTestId("scans-empty")).toContainText("1 of 3 chunks", { timeout: 30_000 });
    expect(queryStatuses).toEqual([]);
    status = "complete";
    await expect(page.getByTestId("scans-finding-row")).toContainText("SQL concat", { timeout: 30_000 });
    expect(queryStatuses.length).toBeGreaterThan(0);
    expect(queryStatuses.every((value) => value === "complete")).toBe(true);
    await expect(page.getByTestId("scans-error")).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Retry loading", exact: true })).toHaveCount(0);
    await expect(page.getByTestId("scans-counts")).toContainText("1 finding");
  });

  for (const hasFindings of [false, true]) {
    webE2e(`partial scan coverage with ${hasFindings ? "available findings" : "empty results"}`, async ({ page, request }) => {
      await installScanRoutes(page, hasFindings ? scanFindings : [], true);
      await bootstrapActiveProject(page, request, undefined, undefined, { contextNav: CONTEXT_NAV_WITH("security") });
      await page.getByTestId("project-security-entry").click();
      await selectRunsView(page);
      await expect(page.getByTestId("scans-issues-trigger")).toContainText("Analysis issues · 14");
      await expect(page.getByTestId("scans-limitations")).toHaveCount(0);
      if (hasFindings) {
        await expect(page.getByTestId("scans-finding-row")).toHaveCount(1);
      } else {
        await expect(page.getByTestId("scans-empty")).toContainText("No findings in analyzed code");
        await expect(page.getByText("No findings in this run.", { exact: true })).toHaveCount(0);
      }
      const diagnostics = page.getByRole("button", { name: "Analysis issues · 14", exact: true });
      await diagnostics.focus();
      await page.keyboard.press("Enter");
      await expect(diagnostics).toHaveAttribute("aria-expanded", "true");
      await expect(page.getByText("src/pipeline-0.py:73:18", { exact: true })).toBeVisible();
      if (hasFindings) {
        await expect(page.getByTestId("scans-finding-row")).toBeVisible();
        const bounds = await page.getByTestId("scans-findings-scroll").boundingBox();
        expect(bounds?.height).toBeGreaterThan(150);
      }
      await page.keyboard.press("Escape");
      await expect(diagnostics).toHaveAttribute("aria-expanded", "false");
      await expect(diagnostics).toBeFocused();
      if (hasFindings) {
        await page.getByTestId("scans-finding-row").click();
        await expect(page.getByTestId("scans-drill-down")).toContainText("opengrep:sql");
      }
      await page.setViewportSize({ width: 800, height: 700 });
      await expect(diagnostics).toBeVisible();
      expect(await page.locator("body").evaluate((body) => body.scrollWidth <= window.innerWidth)).toBe(true);
    });
  }

  webE2e("opens from Context menu, lists locations, drills down, exposes SARIF download", async ({
    page,
    request,
  }) => {
    await installScanRoutes(page);
    await bootstrapActiveProject(
      page,
      request,
      undefined,
      undefined,
      { contextNav: CONTEXT_NAV_WITH("security") },
    );
    await page.getByTestId("project-security-entry").click();
    await selectRunsView(page);

    await expect(page.getByTestId("project-scans-view")).toBeVisible();
    await expect(page.getByTestId("security-findings-pane")).toBeVisible();
    await expect(
      page.getByRole("group", { name: "Filter by severity" }),
    ).toContainText("High 1", { timeout: 15_000 });
    await expect(page.getByTestId("scans-finding-row").first()).toContainText(
      "src/main.go:12",
    );

    await page.getByTestId("scans-finding-row").first().click();
    await expect(page.getByTestId("scans-drill-down")).toContainText("opengrep:sql");
    await expect(page.getByTestId("scans-drill-down")).toContainText("fp-e2e");

    const downloadPromise = page.waitForEvent("download", { timeout: 15_000 });
    const exportTrigger = page.getByTestId("scans-overflow-trigger");
    await exportTrigger.focus();
    await exportTrigger.press("Enter");
    const sarifExport = page.getByTestId("scans-export-run-sarif");
    await expect(sarifExport).toBeVisible();
    await sarifExport.focus();
    await sarifExport.press("Enter");
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toContain(".sarif.json");
  });

  webE2e("keeps pagination visible and rows independently scrollable on short displays", async ({
    page,
    request,
  }) => {
    const findings: SecurityFinding[] = Array.from({ length: 51 }, (_, index) => ({
      rule_id: `opengrep:rule-${index}`,
      level: "high",
      message: `Finding ${index}`,
      locations: [{ uri: `src/file-${index}.go`, start_line: index + 1 }],
      fingerprints: { primary: `fp-e2e-${index}` },
      tool: { driver_id: "opengrep", name: "OpenGrep" },
      properties: { lycaon: { hint_code: `SCAN_${index}` } },
    }));

    await installScanRoutes(page, findings);
    await bootstrapActiveProject(
      page,
      request,
      undefined,
      undefined,
      { contextNav: CONTEXT_NAV_WITH("security") },
    );
    await page.getByTestId("project-security-entry").click();
    await selectRunsView(page);
    await page.setViewportSize({ width: 1024, height: 600 });

    const pager = page.getByTestId("scans-findings-pager");
    const scroller = page.getByTestId("scans-findings-scroll").locator(":scope > .den-scrollport__viewport");
    await expect(pager).toBeVisible();
    await expect(page.getByTestId("scans-finding-row")).toHaveCount(25);

    for (const appearance of ["light", "dark"] as const) {
      await page.evaluate((value) => {
        document.documentElement.dataset.denAppearance = value;
      }, appearance);

      const layout = await page.evaluate(() => {
        const pane = document.querySelector<HTMLElement>(".den-scans-list-pane");
        const rowsHost = document.querySelector<HTMLElement>(
          ".den-scans-list-scroll > .den-scrollport__viewport",
        );
        const footer = document.querySelector<HTMLElement>(
          ".den-scans-list-pane > .den-table-pager",
        );
        const main = document.querySelector<HTMLElement>(
          ".project-scans-view .den-browse-main",
        );
        if (!pane || !rowsHost || !footer || !main) {
          throw new Error("findings layout missing");
        }
        const paneRect = pane.getBoundingClientRect();
        const rowsRect = rowsHost.getBoundingClientRect();
        const footerRect = footer.getBoundingClientRect();
        return {
          paneDisplay: getComputedStyle(pane).display,
          paneOverflow: getComputedStyle(pane).overflow,
          rowsOverflowY: getComputedStyle(rowsHost).overflowY,
          rowsScrollable: rowsHost.scrollHeight > rowsHost.clientHeight,
          mainOverflowPx: main.scrollHeight - main.clientHeight,
          footerBottom: footerRect.bottom,
          paneBottom: paneRect.bottom,
          viewportBottom: window.innerHeight,
          rowsBottom: rowsRect.bottom,
          footerTop: footerRect.top,
        };
      });

      expect(layout.paneDisplay).toBe("flex");
      expect(layout.paneOverflow).toBe("hidden");
      expect(["auto", "scroll"]).toContain(layout.rowsOverflowY);
      expect(layout.rowsScrollable).toBe(true);
      expect(layout.mainOverflowPx).toBeLessThanOrEqual(1);
      expect(layout.footerBottom).toBeLessThanOrEqual(layout.paneBottom + 1);
      expect(layout.footerBottom).toBeLessThanOrEqual(layout.viewportBottom + 1);
      expect(Math.abs(layout.rowsBottom - layout.footerTop)).toBeLessThanOrEqual(
        1,
      );
    }

    await scroller.evaluate((element) => {
      element.scrollTop = element.scrollHeight;
    });
    await pager.getByRole("button", { name: "Next" }).click();
    await expect(page.getByTestId("scans-finding-row").first()).toContainText(
      "Finding 25",
    );
    await expect(pager).toContainText("26–50 of 51");
    await expect
      .poll(() =>
        scroller.evaluate((element) => {
          return element.scrollTop;
        }),
      )
      .toBe(0);
  });
  webE2e("adds selected security findings to one chat attachment without sending", async ({ page, request }) => {
    const findings = [scanFindings[0]!, {
      ...scanFindings[0]!,
      message: "Another SQL concat",
      locations: [],
      fingerprints: { primary: "fp-second" },
    }];
    await installScanRoutes(page, findings);
    await bootstrapChatSession(page, request, { contextNav: CONTEXT_NAV_WITH("security") });
    const composer = await waitForChatComposerReady(page);
    await composer.fill("Help me understand these findings.");
    const sends: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && /\/v1\/sessions\/[^/]+\/prompts$/.test(req.url())) sends.push(req.url());
    });
    await page.getByTestId("project-security-entry").click();
    await expect(page.getByTestId("ledger-row")).toHaveCount(2);
    await page.getByTestId("ledger-select-all").click();
    await expect(page.getByTestId("ledger-selection")).toContainText("2 selected");
    await page.getByTestId("ledger-selection-add-to-chat").click();
    const chooser = page.getByRole("dialog", { name: "Choose a chat" });
    await expect(chooser).toBeVisible();
    const upload = page.waitForRequest((req) => req.method() === "POST" && /\/v1\/projects\/[^/]+\/attachments(?:\?|$)/.test(req.url()));
    await chooser.getByRole("button").filter({ hasText: "Current chat" }).click();
    const body = (await upload).postDataBuffer()?.toString() ?? "";
    expect(body).toContain("fp-e2e");
    expect(body).toContain("fp-second");
    const chips = liveChatStage(page).getByTestId("composer-attachment-chip");
    await expect(chips).toHaveCount(1);
    await expect(chips).toContainText("security-findings.txt");
    await expect(chooser).toHaveCount(0);
    await expect(composer).toHaveValue("Help me understand these findings.");
    expect(sends).toEqual([]);
  });

  webE2e("opens on the project ledger and drills into the run behind a row", async ({
    page,
    request,
  }) => {
    await installScanRoutes(page);
    await bootstrapActiveProject(
      page,
      request,
      undefined,
      undefined,
      { contextNav: CONTEXT_NAV_WITH("security") },
    );
    await page.getByTestId("project-security-entry").click();

    await expect(page.getByTestId("ledger-rows")).toBeVisible();
    await expect(page.getByTestId("scans-run-picker")).toHaveCount(0);
    await expect(page.getByTestId("ledger-row").first()).toContainText("SQL concat");
    await expect(page.getByTestId("ledger-counts")).toContainText("1 open");
    await page.getByTestId("ledger-tab-all").click();
    await page.getByTestId("ledger-filter").click();
    await expect(page.getByTestId("ledger-filter-state-not_observed")).toBeVisible();
    await page.keyboard.press("Escape");

    await page.getByTestId("ledger-row").first().click();
    await expect(page.getByTestId("ledger-detail")).toContainText("opengrep:sql");
    await expect(page.getByTestId("ledger-fix-with-agent")).toBeVisible();

    await page.getByTestId("ledger-open-run").click();
    await expect(page.getByTestId("scans-run-picker")).toBeVisible();
    await expect(page.getByTestId("scans-finding-row").first()).toContainText(
      "src/main.go:12",
    );
  });
});
