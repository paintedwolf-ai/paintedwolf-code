import type { ProjectListResponse } from "../src/api/types.ts";
import { expect } from "@playwright/test";
import { mockedBackendE2e, seedAppState } from "./helpers.ts";

/** Exercises recovery UI with an isolated store. */
mockedBackendE2e.describe("store incompatible recovery", () => {
  mockedBackendE2e("unavailable readiness endpoints do not form a retry storm", async ({ page }) => {
    let unavailableRequests = 0;
    await page.route("**/health", (route) => route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        status: "recovery", version: "0.0.0-test", store_revision: 1,
        schema_version: 1, store_schema_version: 5,
        recovery_reason: "schema_mismatch", recovery_snapshot_available: false,
      }),
    }));
    await page.route("**/v1/**", (route) => {
      unavailableRequests++;
      return route.fulfill({
        status: 503, contentType: "application/json",
        body: JSON.stringify({ code: "store_incompatible", message: "Store incompatible" }),
      });
    });
    await seedAppState(page, { onboarding: { firstRunSetupCompleted: true } });
    await page.goto("/");
    await expect(page.getByTestId("critical-stop")).toHaveAttribute("data-code", "store_incompatible", { timeout: 30_000 });
    // Observe a retry interval: a reachable 503 must not recursively start another read.
    await page.waitForTimeout(2_000);
    expect(unavailableRequests).toBeLessThanOrEqual(12);
    await expect(page.getByTestId("critical-stop-secondary")).toBeEnabled();
  });

  mockedBackendE2e(
    "schema recovery stays centered and completes in place",
    async ({ page }) => {
      const snapshotAt = "2026-08-01T10:14:22Z";
      let restored = false;
      await page.route("**/health", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(
            restored
              ? {
                  status: "ok",
                  version: "0.0.0-test",
                  store_revision: 2,
                  schema_version: 1,
                }
              : {
                  status: "recovery",
                  version: "0.0.0-test",
                  store_revision: 1,
                  schema_version: 1,
                  store_schema_version: 5,
                  recovery_reason: "schema_mismatch",
                  recovery_detail: "schema_version 5 does not match 1",
                  recovery_snapshot_available: true,
                  recovery_snapshot_at: snapshotAt,
                },
          ),
        });
      });
      await page.route("**/v1/backup/restore/recovery-snapshot", async (route) => {
        restored = true;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            restart_required: true,
            recovery_copy_path: "/tmp/restore-recovery",
          }),
        });
      });
      await page.route("**/v1/preflight", async (route) => {
        await route.fulfill({
          status: 503,
          contentType: "application/json",
          body: JSON.stringify({
            code: "store_incompatible",
            message: "store incompatible",
          }),
        });
      });
      await page.route("**/v1/projects**", async (route) => {
        await route.fulfill(
          restored
            ? { status: 200, json: { projects: [] } satisfies ProjectListResponse }
            : {
                status: 503,
                contentType: "application/json",
                body: JSON.stringify({
                  code: "store_incompatible",
                  message: "store incompatible",
                }),
              },
        );
      });

      await seedAppState(page, {
        onboarding: { firstRunSetupCompleted: true },
      });
      await page.setViewportSize({ width: 1280, height: 1000 });
      await page.goto("/");

      const stop = page.getByTestId("critical-stop");
      await expect(stop).toBeVisible({ timeout: 30_000 });
      await expect(stop).toHaveAttribute("data-code", "store_incompatible");
      await expect(stop.locator(".den-critical-stop__title")).toHaveText(
        "This app can't open your data",
      );
      const when = new Date(snapshotAt).toLocaleString(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
      });
      await expect(stop).toContainText(when);
      await expect(stop).toContainText("different data format");
      await expect(stop).toContainText("expected schema 1 · found schema 5");
      await expect(page.getByTestId("critical-stop-diagnostic")).toHaveText(
        "schema_version 5 does not match 1",
      );
      await expect(page.getByTestId("critical-stop-primary")).toHaveText(
        "Restore snapshot",
      );
      await expect(page.getByTestId("critical-stop-secondary")).toHaveText(
        "Start fresh…",
      );

      const hostBox = await page.getByTestId("critical-stop-host").boundingBox();
      const stopBox = await stop.boundingBox();
      expect(hostBox).not.toBeNull();
      expect(stopBox).not.toBeNull();
      const hostCenter = hostBox!.y + hostBox!.height / 2;
      const stopCenter = stopBox!.y + stopBox!.height / 2;
      expect(Math.abs(stopCenter - hostCenter)).toBeLessThanOrEqual(2);

      await page.getByTestId("critical-stop-primary").click();
      // Browser reconnect includes runtime discovery.
      await expect(stop).toHaveCount(0, { timeout: 15_000 });
    },
  );

  mockedBackendE2e(
    "fresh start confirms, stages, and completes in place",
    async ({ page }) => {
      let reset = false;
      let resetRequests = 0;
      await page.route("**/health", async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(
            reset
              ? {
                  status: "ok",
                  version: "0.0.0-test",
                  store_revision: 2,
                  schema_version: 1,
                }
              : {
                  status: "recovery",
                  version: "0.0.0-test",
                  store_revision: 1,
                  schema_version: 1,
                  store_schema_version: 5,
                  recovery_reason: "schema_mismatch",
                  recovery_detail: "schema_version 5 does not match 1",
                  recovery_snapshot_available: false,
                },
          ),
        });
      });
      await page.route("**/v1/store/reset", async (route) => {
        resetRequests += 1;
        reset = true;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            restart_required: true,
            recovery_copy_path: "/tmp/fresh-start-recovery",
          }),
        });
      });
      await page.route("**/v1/preflight", async (route) => {
        await route.fulfill({
          status: reset ? 200 : 503,
          contentType: "application/json",
          body: reset
            ? JSON.stringify({ overall: "ok", probes: [] })
            : JSON.stringify({
                code: "store_incompatible",
                message: "store incompatible",
              }),
        });
      });
      await page.route("**/v1/projects**", async (route) => {
        await route.fulfill(
          reset
            ? { status: 200, json: { projects: [] } satisfies ProjectListResponse }
            : {
                status: 503,
                contentType: "application/json",
                body: JSON.stringify({
                  code: "store_incompatible",
                  message: "store incompatible",
                }),
              },
        );
      });

      await seedAppState(page, {
        onboarding: { firstRunSetupCompleted: true },
      });
      await page.goto("/");

      const stop = page.getByTestId("critical-stop");
      await expect(stop).toBeVisible({ timeout: 30_000 });
      await page.getByTestId("critical-stop-secondary").click();
      const dialog = page.getByTestId("confirm-destructive-dialog");
      await expect(dialog).toBeVisible();
      await expect(dialog).toContainText("A recovery copy is saved first.");
      await expect(page.getByTestId("confirm-destructive-ok")).toHaveText(
        "Start fresh",
      );
      await page.getByTestId("confirm-destructive-ok").click();

      await expect.poll(() => resetRequests).toBe(1);
      await expect(stop).toHaveCount(0, { timeout: 15_000 });
    },
  );
});
