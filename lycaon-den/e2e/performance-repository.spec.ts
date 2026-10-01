import { copyFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { expect } from "@playwright/test";
import { activateProject, apiConfig, apiCreateProjectWithRoot, apiRemoveProject, e2eTempDir, gotoShell, modelIndependentWebE2e as test } from "./helpers.ts";

test("outlines concurrent patch revisions without parser budget failures", async ({ request }, info) => {
  const root = e2eTempDir(request, "patch-outlines");
  const patch = readFileSync(new URL("../patches/overlayscrollbars@2.16.0.patch", import.meta.url), "utf8");
  const copies = [1, 1, 1, 2, 4, 8];
  copies.forEach((count, index) => writeFileSync(path.join(root, `change-${index}.patch`), patch.repeat(count)));
  const project = await apiCreateProjectWithRoot(request, root, "Patch outlines");
  const { apiUrl, token } = apiConfig();
  const samples = await Promise.all(copies.map(async (copies, index) => {
    const start = performance.now();
    const response = await request.get(`${apiUrl}/v1/projects/${project.id}/source/symbols`, {
      headers: { Authorization: `Bearer ${token}` }, params: { root_id: project.roots[0]!.id, path: `change-${index}.patch` },
    });
    const result = await response.json();
    expect(response.ok(), JSON.stringify(result)).toBe(true);
    expect(result.symbols).toHaveLength(4 * copies);
    return { copies, durationMs: performance.now() - start, symbols: result.symbols.length };
  }));
  writeFileSync(info.outputPath("patch-outlines.json"), JSON.stringify(samples, null, 2));
});

const roots = (process.env.PW_PERFORMANCE_ROOTS ?? "").split(path.delimiter).filter(Boolean);
for (const root of roots) {
  test(`opens and revisits real files in ${path.basename(root)} during repository discovery`, async ({ page, request }, info) => {
    info.setTimeout(240_000);
    const names = ["README.md", "README.txt", "moz.build", "package.json", "Taskfile.yml", "AUTHORS", "LICENSE", "AGENTS.md"].filter(name => existsSync(path.join(root, name)));
    expect(names.length).toBeGreaterThan(2);
    const project = await apiCreateProjectWithRoot(request, root, `Repository performance ${path.basename(root)}`);
    const { apiUrl, token } = apiConfig();
    const trustSamples: { durationMs: number; status: number; bytes: number; error?: string }[] = [];
    const trust = async () => {
      const start = performance.now();
      const response = await request.get(`${apiUrl}/v1/projects/${project.id}/trust`, { headers: { Authorization: `Bearer ${token}` }, timeout: 120_000 });
      const body = await response.body();
      trustSamples.push({ durationMs: performance.now() - start, status: response.status(), bytes: body.length,
        ...(response.ok() ? {} : { error: body.toString() }) });
    };
    const coldStarted = performance.now();
    const coldTrust = trust().catch(error => trustSamples.push({ durationMs: performance.now() - coldStarted, status: 0, bytes: 0, error: String(error) }));
    const samples: { file: string; elapsedMs: number; handlerMs: number; pass: number; readyForInputMs?: number }[] = [];
    let deletion: { durationMs: number; projectState?: string } | undefined;
    try {
      await gotoShell(page);
      await activateProject(page, project.id);
      await page.getByTestId("project-files-entry").click();
      await expect(page.getByTestId("files-tree")).toBeVisible();
      for (let pass = 0; pass < 3; pass++) for (const file of names) {
        const started = performance.now();
        const handlerMs = await page.evaluate(async ({ projectId, rootId, file }) => {
          const moduleUrl = "/src/platform/navigation/open-source.ts";
          const { openSourceLocation } = await import(/* @vite-ignore */ moduleUrl) as typeof import("../src/platform/navigation/open-source.ts");
          const start = performance.now();
          void openSourceLocation({ projectId, rootId, path: file, intent: "permanent" });
          return performance.now() - start;
        }, { projectId: project.id, rootId: project.roots[0]!.id, file });
        const tab = page.locator(`[data-testid="files-tab"][data-path="${file}"]`);
        await expect(tab).toHaveAttribute("aria-selected", "true", { timeout: 30_000 });
        await expect(tab).toHaveAttribute("aria-busy", "false", { timeout: 30_000 });
        const content = page.locator('[data-testid="files-editor-host"] .cm-content:visible');
        await expect(content).toBeVisible();
        const sample = { file, elapsedMs: performance.now() - started, handlerMs, pass, readyForInputMs: undefined as number | undefined };
        if (file === names[0]) {
          await expect(content).toHaveAttribute("contenteditable", "true", { timeout: 30_000 });
          sample.readyForInputMs = performance.now() - started;
        }
        samples.push(sample);
      }
      await coldTrust;
      if (trustSamples[0]?.status === 200) for (let n = 0; n < 5; n++) await trust();
      await page.screenshot({ path: info.outputPath("repository-navigation.png") });
      const deleteStarted = performance.now();
      const removal = await apiRemoveProject(request, project.id);
      deletion = { durationMs: performance.now() - deleteStarted, projectState: removal?.project_state };
      expect(deletion.projectState).toBe("deleted");
      expect(deletion.durationMs).toBeLessThan(5000);
      expect(trustSamples.every(sample => sample.status === 200), JSON.stringify(trustSamples)).toBe(true);
      expect(Math.max(...samples.map(sample => sample.handlerMs))).toBeLessThan(100);
      expect(Math.max(...trustSamples.slice(1).map(sample => sample.durationMs))).toBeLessThan(1000);
    } finally {
      writeFileSync(info.outputPath("repository-performance.json"), JSON.stringify({ root, samples, trustSamples, deletion }, null, 2));
      const stateDir = process.env.LYCAON_E2E_STATE_DIR;
      const log = stateDir && path.join(stateDir, "sidecar.log");
      if (log && existsSync(log)) copyFileSync(log, info.outputPath("sidecar.log"));
    }
  });
}
