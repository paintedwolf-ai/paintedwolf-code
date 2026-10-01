import path from "node:path";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import {
  DEFAULT_CONTEXT_NAV_VISIBLE,
  type ContextNavItemId,
} from "../shared/app-state-types.ts";
import {
  APP_STATE_STORAGE_KEY,
  APP_STATE_STORAGE_SLICE_PREFIX,
} from "../shared/app-state-storage.ts";
import {
  LIVE_CHAT_STAGE_SELECTOR,
} from "../shared/stage-selectors.ts";
import type {
  ActiveWorkflowRunResponse,
  AttachmentUploadResponse,
  Blueprint,
  BlueprintListResponse,
  CreateBlueprintRequest,
  Message,
  ModelPolicy,
  ProjectRemovalAssessment,
  ProjectRemovalResult,
  Project,
  ProjectListResponse,
  PromptRequest,
  ProviderListResponse,
  ProviderMeta,
  Session,
  SessionTranscriptPage,
  StartWorkflowRunRequest,
  WorkflowRun,
} from "../src/api/types.ts";

const tier = process.env.PLAYWRIGHT_E2E;
const runWeb = tier === "web" || tier === "desktop";

const RENDERER_LIVENESS_TIMEOUT_MS = 5_000;
let appStateSeedSequence = 0;
const createdProjects = new WeakMap<APIRequestContext, Set<string>>();
const createdTempDirs = new WeakMap<APIRequestContext, Set<string>>();

async function assertRendererYields(page: Page): Promise<void> {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([
      page.evaluate(
        () =>
          new Promise<"hidden" | "yielded">((resolve) => {
            // Background pages may throttle animation frames.
            if (document.visibilityState !== "visible") {
              resolve("hidden");
              return;
            }
            requestAnimationFrame(() => requestAnimationFrame(() => resolve("yielded")));
          }),
      ),
      new Promise<never>((_, reject) => {
        timeout = setTimeout(
          () =>
            reject(
              new Error(
                "Renderer did not yield two animation frames; a synchronous or microtask loop is blocking the UI.",
              ),
            ),
          RENDERER_LIVENESS_TIMEOUT_MS,
        );
      }),
    ]);
  } finally {
    if (timeout !== undefined) clearTimeout(timeout);
  }
}

const livenessE2e = test.extend<{ rendererLiveness: void }>({
  rendererLiveness: [
    async ({ page }, use, testInfo) => {
      await use();
      // Renderer diagnostics preserve unexpected test failures.
      if (testInfo.status === testInfo.expectedStatus && !page.isClosed()) {
        await assertRendererYields(page);
      }
    },
    { auto: true },
  ],
});

/** Minimal on-disk repo for project attach E2E. */
export const E2E_FIXTURE_PROJECT =
  process.env.LYCAON_E2E_PROJECT_DIR ??
  path.resolve(
    fileURLToPath(new URL("../../lycaon/test/fixtures/e2e/minimal-go-project", import.meta.url)),
  );

/** The published example pack that declares every contribution kind. */
export const E2E_EXAMPLE_PACK_DIR = path.resolve(
  fileURLToPath(
    new URL("../../lycaon/config/fixtures/example-packs/editor-surface", import.meta.url),
  ),
);

export function apiConfig() {
  return {
    apiUrl: process.env.LYCAON_E2E_API_URL ?? "http://127.0.0.1:8787",
    token: process.env.LYCAON_E2E_TOKEN ?? "e2e-playwright-token",
  };
}

/** Waits until the in-page harness has established its sidecar connection. */
export async function waitHarnessConnected(page: Page): Promise<void> {
  await page.waitForFunction(
    () =>
      (window as unknown as { __harness?: { state(): { connected: boolean } } })
        .__harness?.state().connected === true,
    undefined,
    { timeout: 60_000 },
  );
}

/** Waits for the session's active workflow run and returns its stable id. */
export async function waitActiveWorkflowRun(
  request: APIRequestContext,
  sessionId: string,
): Promise<{ id: string }> {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  let runId = "";
  await expect
    .poll(
      async () => {
        const response = await request.get(
          `${apiUrl}/v1/sessions/${sessionId}/workflow-runs/active`,
          { headers },
        );
        if (!response.ok()) return null;
        const { run } = (await response.json()) as ActiveWorkflowRunResponse;
        runId = run?.id ?? "";
        return runId || null;
      },
      { timeout: 60_000 },
    )
    .toBeTruthy();
  return { id: runId };
}

export async function waitForSessionReady(
  request: APIRequestContext,
  sessionId: string,
) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  await expect
    .poll(
      async () => {
        const response = await request.get(`${apiUrl}/v1/sessions/${sessionId}`, {
          headers,
        });
        if (!response.ok()) return false;
        const session = (await response.json()) as Session;
        if (session.status === "error") {
          throw new Error("Session failed while preparing.");
        }
        return session.status !== "preparing";
      },
      { timeout: 60_000 },
    )
    .toBe(true);
}

/** Each prompt submission gets a distinct operation ID. */
export async function apiPostPrompt(
  request: APIRequestContext,
  sessionId: string,
  prompt: Omit<PromptRequest, "operation_id">,
) {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  await waitForSessionReady(request, sessionId);
  return request.post(`${apiUrl}/v1/sessions/${sessionId}/prompts`, {
    headers: {
      ...headers,
      "Content-Type": "application/json",
    },
    data: { operation_id: crypto.randomUUID(), ...prompt },
  });
}

/** A held prompt request exposes optimistic UI state. */
export async function holdPromptRequest(
  page: Page,
  text: string,
): Promise<{ intercepted: Promise<void>; release: () => void }> {
  let release!: () => void;
  let markIntercepted!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const intercepted = new Promise<void>((resolve) => {
    markIntercepted = resolve;
  });
  await page.route("**/v1/sessions/*/prompts", async (route) => {
    let prompt: { text?: unknown } = {};
    try {
      prompt = route.request().postDataJSON() as { text?: unknown };
    } catch {
      await route.continue();
      return;
    }
    if (prompt.text === text) {
      markIntercepted();
      await gate;
    }
    await route.continue();
  });
  return { intercepted, release };
}

/** Stream bytes to the attachment route and return the host-derived receipt. */
export async function apiUploadAttachment(
  request: APIRequestContext,
  projectId: string,
  filename: string,
  mime: string,
  bytes: Buffer,
): Promise<AttachmentUploadResponse> {
  const { apiUrl, token } = apiConfig();
  const response = await request.post(
    `${apiUrl}/v1/projects/${projectId}/attachments?filename=${encodeURIComponent(filename)}&mime=${encodeURIComponent(mime)}`,
    {
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/octet-stream",
      },
      data: bytes,
    },
  );
  expect(response.ok(), await response.text()).toBeTruthy();
  return response.json() as Promise<AttachmentUploadResponse>;
}

export function e2eUniqueLabel(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}`;
}

/** Registers a project the UI created so the fixture deletes it with the API-created ones. */
export function trackCreatedProject(request: APIRequestContext, projectId: string): void {
  createdProjects.get(request)?.add(projectId);
}

export async function apiListProjects(
  request: APIRequestContext,
): Promise<Project[]> {
  const { projects } = await apiJson<ProjectListResponse>(request, "GET", "/v1/projects");
  return projects;
}

/** Removes a project and its attached data through the assessed removal flow. */
export async function apiRemoveProject(
  request: APIRequestContext,
  projectId: string,
): Promise<ProjectRemovalResult | undefined> {
  const { apiUrl, token } = apiConfig();
  const headers = { Authorization: `Bearer ${token}` };
  const assessed = await request.get(`${apiUrl}/v1/projects/${projectId}/removal-assessment`, { headers });
  if (!assessed.ok()) {
    const body = (await assessed.json().catch(() => ({}))) as { code?: string };
    if (body.code === "project_not_found") return undefined;
    expect(assessed.ok(), JSON.stringify(body)).toBeTruthy();
  }
  const assessment = (await assessed.json()) as ProjectRemovalAssessment;
  return apiJson<ProjectRemovalResult>(request, "POST", `/v1/projects/${projectId}/removals`, {
    operation_id: crypto.randomUUID(),
    assessment_token: assessment.assessment_token,
    remove_extensions: [],
    force: true,
  });
}

/** Seeds current persisted app state before navigation. */
export async function seedAppState(
  page: Page,
  partial?: {
    lastActiveProjectId?: string;
    /** Device context navigation pins. */
    contextNav?: { visible?: string[] };
    /** Defaults to completed for shell tests. */
    onboarding?: { firstRunSetupCompleted?: boolean };
    /** Optional What's New latch. */
    whatsNew?: { lastSeenVersion?: string };
    /** Contextual tips default off. */
    firstTimeTips?: { enabled?: boolean; dismissed?: string[] };
    /** Enables draft rows and benign tool cards. */
    debug?: { verboseMode?: boolean };
  },
) {
  const onboarding = {
    firstRunSetupCompleted:
      partial?.onboarding?.firstRunSetupCompleted ?? true,
  };
  const firstTimeTips = {
    enabled: partial?.firstTimeTips?.enabled ?? false,
    ...(partial?.firstTimeTips?.dismissed
      ? { dismissed: partial.firstTimeTips.dismissed }
      : {}),
  };
  // Each seed applies once, with explicit seeds taking priority.
  const priority = ++appStateSeedSequence;
  const marker = `${APP_STATE_STORAGE_KEY}.e2e-seed-${priority}`;
  const priorityKey = `${APP_STATE_STORAGE_KEY}.e2e-seed-priority`;
  await page.addInitScript(
    ({
      slicePrefix,
      markerKey,
      priorityKey: appliedPriorityKey,
      seedPriority,
      partialState,
      onboardingState,
      whatsNewState,
      firstTimeTipsState,
      debugState,
    }) => {
      const appliedPriority = Number(sessionStorage.getItem(appliedPriorityKey) ?? "0");
      if (sessionStorage.getItem(markerKey) === "1" || appliedPriority >= seedPriority) {
        sessionStorage.setItem(markerKey, "1");
        return;
      }
      sessionStorage.setItem(markerKey, "1");
      sessionStorage.setItem(appliedPriorityKey, String(seedPriority));
      const state = {
        recents: [],
        onboarding: onboardingState,
        firstTimeTips: firstTimeTipsState,
        ...(whatsNewState ? { whatsNew: whatsNewState } : {}),
        ...(partialState?.lastActiveProjectId
          ? { lastActiveProjectId: partialState.lastActiveProjectId }
          : {}),
        ...(partialState?.contextNav
          ? { contextNav: partialState.contextNav }
          : {}),
        ...(debugState ? { debug: debugState } : {}),
      };
      for (const [slice, value] of Object.entries(state)) {
        localStorage.setItem(
          `${slicePrefix}${encodeURIComponent(slice)}`,
          JSON.stringify(value),
        );
      }
    },
    {
      slicePrefix: APP_STATE_STORAGE_SLICE_PREFIX,
      markerKey: marker,
      priorityKey,
      seedPriority: priority,
      partialState: {
        lastActiveProjectId: partial?.lastActiveProjectId,
        contextNav: partial?.contextNav,
      },
      onboardingState: onboarding,
      whatsNewState: partial?.whatsNew,
      firstTimeTipsState: firstTimeTips,
      debugState: partial?.debug,
    },
  );
}

/** Adds entries to the default context navigation. */
export const CONTEXT_NAV_WITH = (...extra: ContextNavItemId[]) => {
  const seen = new Set<ContextNavItemId>(DEFAULT_CONTEXT_NAV_VISIBLE);
  const visible = [...DEFAULT_CONTEXT_NAV_VISIBLE];
  for (const id of extra) {
    if (seen.has(id)) continue;
    seen.add(id);
    visible.push(id);
  }
  return { visible };
};

export async function seedFirstRunUnset(page: Page) {
  await seedAppState(page, {
    onboarding: { firstRunSetupCompleted: false },
  });
}

const stableAppStateE2e = livenessE2e.extend<{ stableAppState: void; requiresDefaultModel: boolean }>({
  requiresDefaultModel: [true, { option: true }],
  stableAppState: [
    async ({ page, request, requiresDefaultModel }, use) => {
      createdProjects.set(request, new Set());
      createdTempDirs.set(request, new Set());
      if (process.env.LYCAON_LLM_MANUAL === "1") {
        const { apiUrl, token } = apiConfig();
        const response = await request.post(`${apiUrl}/harness/llm/auto`, {
          headers: { Authorization: `Bearer ${token}` },
          data: { enabled: true, text: "Harness reply." },
        });
        expect(response.ok(), await response.text()).toBeTruthy();
      }
      if (requiresDefaultModel) await apiEnsureDefaultModel(request);
      // Shell specs start after setup with contextual tips disabled.
      await seedAppState(page);
      try {
        await use();
      } finally {
        try {
          const ids = [...(createdProjects.get(request) ?? [])].reverse();
          for (const id of ids) await apiRemoveProject(request, id);
        } finally {
          createdProjects.delete(request);
          for (const dir of [...(createdTempDirs.get(request) ?? [])].reverse()) {
            rmSync(dir, { recursive: true, force: true });
          }
          createdTempDirs.delete(request);
        }
      }
    },
    { auto: true },
  ],
});

export const mockedBackendE2e = runWeb
  ? livenessE2e
  : (test.skip as unknown as typeof livenessE2e);

/** Starts E2E after onboarding. */
export const webE2e = runWeb
  ? stableAppStateE2e
  : (test.skip as unknown as typeof stableAppStateE2e);

const modelIndependentAppStateE2e = stableAppStateE2e.extend({ requiresDefaultModel: false });

/** Keeps shell setup and cleanup without requiring model discovery. */
export const modelIndependentWebE2e = runWeb
  ? modelIndependentAppStateE2e
  : (test.skip as unknown as typeof modelIndependentAppStateE2e);

export async function apiCreateProjectWithRoot(
  request: APIRequestContext,
  rootPath: string,
  name = "E2E Project",
): Promise<Project> {
  const { apiUrl, token } = apiConfig();
  const res = await request.post(`${apiUrl}/v1/projects`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: { name, roots: [{ path: rootPath }] },
  });
  expect(res.ok()).toBeTruthy();
  const project = (await res.json()) as Project;
  createdProjects.get(request)?.add(project.id);
  return project;
}

export function e2eTempDir(
  request: APIRequestContext,
  prefix: string,
): string {
  const base = process.env.LYCAON_E2E_STATE_DIR?.trim() || tmpdir();
  mkdirSync(base, { recursive: true });
  const dir = mkdtempSync(path.join(base, `${prefix}-`));
  createdTempDirs.get(request)?.add(dir);
  return dir;
}

/** Creates a disposable project, seeds its root, and opens the Files stage. */
export async function openProjectFilesFixture(
  page: Page,
  request: APIRequestContext,
  options: {
    prefix: string;
    name: string;
    seed(root: string): void;
    readyTestId?: string;
  },
): Promise<{ project: Project; root: string }> {
  const root = e2eTempDir(request, options.prefix);
  options.seed(root);
  const project = await apiCreateProjectWithRoot(
    request,
    root,
    e2eUniqueLabel(options.name),
  );
  await gotoShell(page);
  await activateProject(page, project.id);
  await page.getByTestId("project-files-entry").click();
  await expect(page.getByTestId(options.readyTestId ?? "files-tree")).toBeVisible({
    timeout: 15_000,
  });
  return { project, root };
}

export async function apiAttachProjectRoot(
  request: APIRequestContext,
  projectId: string,
  rootPath: string,
  label?: string,
): Promise<Project> {
  const { apiUrl, token } = apiConfig();
  const res = await request.post(`${apiUrl}/v1/projects/${projectId}/roots`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: { path: rootPath, ...(label ? { label } : {}) },
  });
  expect(res.ok()).toBeTruthy();
  return (await res.json()) as Project;
}

/** Wait until the main shell and either valid navigation branch are mounted. */
export async function expectShellReady(page: Page) {
  await expect(page.getByTestId("shell")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("shell")).toHaveAttribute(
    "data-contributions-ready",
    "true",
    { timeout: 60_000 },
  );
  await expect
    .poll(
      () =>
        page
          .locator('[data-testid="home-nav"]:visible, [data-testid="focused-project-nav"]:visible')
          .count(),
      { timeout: 60_000 },
    )
    .toBeGreaterThan(0);
}

export async function waitForProjectList(page: Page) {
  await page.waitForResponse(
    (resp) =>
      resp.url().includes("/v1/projects") &&
      resp.request().method() === "GET" &&
      resp.ok(),
    { timeout: 60_000 },
  );
}

/** Navigate to shell and wait until the project list has loaded from the sidecar. */
export async function gotoShell(page: Page) {
  const listPromise = waitForProjectList(page);
  await page.goto("/");
  await expectShellReady(page);
  await listPromise;
}

/** Sequential key events update controlled field signals. */
export async function fillSolidControl(locator: Locator, value: string) {
  await locator.click();
  await locator.press("ControlOrMeta+a");
  await locator.press("Backspace");
  await locator.pressSequentially(value, { delay: 5 });
}

export async function waitForProjectButton(page: Page, name: string) {
  await expect(
    page.getByTestId("home-grid").getByText(name, { exact: false }),
  ).toBeVisible({ timeout: 60_000 });
}

/** Open a project from the home grid by clicking its card. */
export async function activateProject(page: Page, projectId: string) {
  const card = page.getByTestId(`project-card-${projectId}`);
  const recentCardReady = await card
    .waitFor({ state: "visible", timeout: 10_000 })
    .then(() => true, () => false);
  if (recentCardReady) {
    await card.getByRole("button").first().click();
  } else {
    const row = await findProjectListRow(page, projectId);
    await row.locator(".project-list-row__name").click();
  }
  const nav = page.getByTestId("focused-project-nav");
  await expect(nav).toBeVisible({ timeout: 30_000 });
  await expect(page.getByTestId("shell")).toHaveAttribute(
    "data-contributions-ready",
    "true",
    { timeout: 60_000 },
  );
}

export async function findProjectListRow(page: Page, projectId: string): Promise<Locator> {
  const homeNav = page.getByTestId("home-nav");
  if (!(await homeNav.isVisible().catch(() => false))) {
    await page.getByTestId("nav-brand").click();
  }
  await expect(homeNav).toBeVisible({ timeout: 60_000 });
  const all = page.getByTestId("home-nav-all");
  await all.click();
  await expect(all).toHaveAttribute("aria-current", "page", { timeout: 10_000 });
  await expect(page.getByTestId("home-list")).toBeVisible({ timeout: 60_000 });
  const row = page.getByTestId(`project-list-row-${projectId}`);
  const previous = page.getByTestId("home-list-pager-prev");
  while ((await previous.isVisible().catch(() => false)) && !(await previous.isDisabled())) {
    await previous.click();
  }
  for (;;) {
    if (await row.isVisible().catch(() => false)) return row;
    const next = page.getByTestId("home-list-pager-next");
    if (!(await next.isVisible().catch(() => false)) || (await next.isDisabled())) break;
    await next.click();
  }
  throw new Error(`project ${projectId} was not present in the All Projects table`);
}

/** Locates the foreground chat stage. */
export function liveChatStage(page: Page): Locator {
  return page.locator(LIVE_CHAT_STAGE_SELECTOR);
}

async function liveStageState(
  page: Page,
): Promise<{ sessionId: string; single: boolean }> {
  return page.evaluate(
    (liveStage) => {
      const stages = document.querySelectorAll(liveStage);
      const live = stages[0];
      const stream = live?.querySelector('[data-testid="chat-stream"]');
      return {
        sessionId: (stream?.getAttribute("data-session-id") ?? "").trim(),
        single: stages.length === 1,
      };
    },
    LIVE_CHAT_STAGE_SELECTOR,
  );
}

/** Returns the session id after the stage transition settles. */
export async function settledChatSessionId(page: Page): Promise<string> {
  let settledId = "";
  await expect
    .poll(
      async () => {
        const state = await liveStageState(page);
        if (!state.single || !state.sessionId) return "";
        settledId = state.sessionId;
        return settledId;
      },
      { timeout: 90_000 },
    )
    .toBeTruthy();
  return settledId;
}

/** Selects a focused-project session and waits through the chat cross-fade. */
export async function openSessionRow(page: Page, sessionId: string): Promise<void> {
  if ((await settledChatSessionId(page).catch(() => "")) === sessionId) return;
  const sessionRow = page.locator(
    `[data-testid="focused-session-list"] [data-session-id="${sessionId}"]`,
  );
  await expect(sessionRow).toBeVisible({ timeout: 60_000 });
  await sessionRow.click();
  await expect
    .poll(() => settledChatSessionId(page), { timeout: 60_000 })
    .toBe(sessionId);
}

/** Open one blank chat after project session creation settles. */
export async function openNewChatSession(page: Page): Promise<string> {
  const previous = await settledChatSessionId(page);
  const newChat = page.getByTestId("new-chat-btn");
  if (await newChat.isEnabled()) {
    await newChat.click();
    await expect.poll(() => settledChatSessionId(page), { timeout: 90_000 }).not.toBe(previous);
  }
  await expect(newChat).toHaveClass(/new-chat-btn--current/, { timeout: 90_000 });
  const sessionId = await settledChatSessionId(page);
  await waitForSessionReady(page.request, sessionId);
  await expect
    .poll(
      async () => (await apiTryGetActiveWorkflowRun(page.request, sessionId)) != null,
      { timeout: 60_000 },
    )
    .toBe(true);
  await expect(
    liveChatStage(page).getByTestId("chat-composer"),
  ).toBeVisible({ timeout: 90_000 });
  return sessionId;
}

/** Create a rooted project via API, load shell, and open it. */
export async function bootstrapActiveProject(
  page: Page,
  request: APIRequestContext,
  rootPath = E2E_FIXTURE_PROJECT,
  name = e2eUniqueLabel("E2E"),
  opts?: {
    contextNav?: { visible?: string[] };
    debug?: { verboseMode?: boolean };
  },
) {
  const project = await apiCreateProjectWithRoot(request, rootPath, name);
  if (opts?.contextNav || opts?.debug) {
    await seedAppState(page, { contextNav: opts.contextNav, debug: opts.debug });
  }
  await gotoShell(page);
  await activateProject(page, project.id);
  return project;
}

async function openEmptyChat(page: Page) {
  const sessionId = await settledChatSessionId(page);
  await waitForSessionReady(page.request, sessionId);
  const composer = liveChatStage(page).getByTestId("chat-composer");
  await expect(composer).toBeVisible({ timeout: 90_000 });
}

/** Seed fixture project, load shell, activate project, and open a chat session. */
export async function bootstrapChatSession(
  page: Page,
  request?: APIRequestContext,
  opts?: {
    contextNav?: { visible?: string[] };
    debug?: { verboseMode?: boolean };
  },
) {
  const req = request ?? page.request;
  const project = await bootstrapActiveProject(
    page,
    req,
    E2E_FIXTURE_PROJECT,
    e2eUniqueLabel("E2E"),
    opts,
  );
  await openEmptyChat(page);
  return project;
}

export async function waitForChatComposerReady(page: Page): Promise<Locator> {
  // A session cross-fade can mount two composers.
  const sessionId = await settledChatSessionId(page);
  await waitForSessionReady(page.request, sessionId);
  const composer = liveChatStage(page).getByTestId("chat-composer");
  await expect(composer).toBeEnabled({ timeout: 60_000 });
  await expect(composer).not.toHaveAttribute("placeholder", "Opening chat…", {
    timeout: 60_000,
  });
  return composer;
}

/** Fill and send a chat prompt from the composer (Enter submits). */
export async function sendChatPrompt(page: Page, text: string) {
  const composer = await waitForChatComposerReady(page);
  await composer.fill(text);
  await composer.press("Enter");
  await expect(composer).toHaveValue("", { timeout: 30_000 });
}

export async function openBottomTab(page: Page, tabId: string) {
  const tab = page.getByTestId(`tab-${tabId}`);
  if ((await tab.getAttribute("aria-expanded")) !== "true") await tab.click();
}

export async function openProgressTab(page: Page) {
  await openBottomTab(page, "progress");
}

export async function openWorkflowsTab(page: Page) {
  await openBottomTab(page, "workflows");
}

type TranscriptMessage = SessionTranscriptPage["messages"][number];
type TranscriptProvenance = Pick<
  TranscriptMessage,
  "origin" | "authority" | "trust_tier"
>;
type TranscriptMessageSeed = Omit<TranscriptMessage, keyof TranscriptProvenance | "created_at"> & {
  created_at?: string;
};

/** Maps each role to host-issued transcript provenance. */
function messageProvenance(
  role: TranscriptMessage["role"],
): TranscriptProvenance {
  switch (role) {
    case "user":
      return { origin: "user", authority: "user", trust_tier: "trusted" };
    case "assistant":
      return { origin: "model", authority: "none", trust_tier: "trusted" };
    case "tool":
      return { origin: "tool", authority: "none", trust_tier: "untrusted" };
    case "system":
      return { origin: "host", authority: "system", trust_tier: "trusted" };
  }
}

/** Transcript fixtures carry host provenance. */
export function transcriptMessages(
  seeds: readonly TranscriptMessageSeed[],
): TranscriptMessage[] {
  return seeds.map((seed) => {
    const { created_at = new Date(0).toISOString(), ...rest } = seed;
    return {
      ...rest,
      created_at,
      ...messageProvenance(seed.role),
    };
  });
}

/** Persist harness transcript rows through the real store and SSE append path. */
export async function apiSeedSessionTranscript(
  request: APIRequestContext,
  sessionId: string,
  messages: readonly Message[],
) {
  const { apiUrl, token } = apiConfig();
  const response = await request.post(`${apiUrl}/harness/transcript`, {
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    data: { session_id: sessionId, messages },
  });
  expect(response.ok(), await response.text()).toBeTruthy();
}

/** Mark a harness session with durable untrusted-content provenance. */
export async function apiSeedUntrustedContent(
  request: APIRequestContext,
  sessionId: string,
) {
  await apiJson<Record<string, unknown>>(
    request,
    "POST",
    "/harness/sessions/untrusted-content",
    { session_id: sessionId },
  );
}

/** Canonical plan markdown for plan_stub_valid. */
export const PLAN_STUB_VALID_CONTENT =
  "---\ntitle: Ship it\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nNone (greenfield).\n\n## Approach\n\nship it\n\n<!-- lycaon:tasks\n[{\"id\":\"t1\",\"title\":\"Implement\",\"files\":[],\"verify\":[]}]\n-->";


export async function apiJson<T>(
  request: APIRequestContext,
  method: string,
  path: string,
  data?: unknown,
): Promise<T> {
  const { apiUrl, token } = apiConfig();
  const res = await request.fetch(`${apiUrl}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(data !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    data,
  });
  const raw = await res.text();
  expect(
    res.ok(),
    `${method} ${path}: ${res.status()} ${res.statusText()}${raw ? ` — ${raw}` : ""}`,
  ).toBeTruthy();
  return JSON.parse(raw) as T;
}

export async function apiListProviders(
  request: APIRequestContext,
): Promise<ProviderMeta[]> {
  const { providers } = await apiJson<ProviderListResponse>(request, "GET", "/v1/providers");
  return providers;
}

/** The coordinator assignment when every slot uses a ready model. */
function readyPolicyCoordinator(
  policy: ModelPolicy,
  providers: readonly ProviderMeta[],
): { provider_id: string; model: string } | null {
  const ready = (slot: { provider_id: string; model: string } | undefined) => {
    const provider = providers.find(
      (candidate) => candidate.id === slot?.provider_id,
    );
    return (
      slot !== undefined &&
      provider?.ready_to_assign === true &&
      provider.models?.some((model) => model.id === slot.model) === true
    );
  };
  const coordinator = policy.coordinator;
  if (
    coordinator !== undefined &&
    ready(coordinator) &&
    ready(policy.lite) &&
    policy.agent_pool.models.length > 0 &&
    policy.agent_pool.models.every(ready)
  ) {
    return coordinator;
  }
  return null;
}

/** Ensures the global policy uses ready models. */
export async function apiEnsureDefaultModel(
  request: APIRequestContext,
): Promise<{ providerId: string; model: string }> {
  const current = await apiJson<ModelPolicy>(
    request,
    "GET",
    "/v1/settings/model-policy",
  );
  const initialProviders = await apiListProviders(request);
  const currentCoordinator = readyPolicyCoordinator(current, initialProviders);
  if (currentCoordinator) {
    return {
      providerId: currentCoordinator.provider_id,
      model: currentCoordinator.model,
    };
  }
  let diagnostic = "no provider snapshot";
  const deadline = Date.now() + 60_000;
  for (let attempt = 0; Date.now() < deadline; attempt += 1) {
    const providers = await apiListProviders(request);
    const ready = providers
      .filter((p) => p.ready_to_assign && (p.models?.length ?? 0) > 0)
      .sort((left, right) => left.id.localeCompare(right.id));
    // Credential-free providers keep harness setup deterministic.
    const preferred =
      ready.find((p) => p.requires_api_key === false) ??
      ready[0];
    diagnostic = providers
      .map(
        (p) =>
          `${p.id}[ready=${p.ready_to_assign === true},models=${p.models?.length ?? 0}]`,
      )
      .join(", ");
    if (preferred) {
      const providerId = preferred.id;
      const model = [...preferred.models!]
        .sort((left, right) => left.id.localeCompare(right.id))[0]!.id;
      const policy: ModelPolicy = {
        coordinator: { provider_id: providerId, model },
        lite: { provider_id: providerId, model },
        agent_pool: {
          selection: "first",
          models: [{ provider_id: providerId, model }],
        },
      };
      try {
        await apiJson(request, "PATCH", "/v1/settings/model-policy", policy);
        const saved = await apiJson<ModelPolicy>(
          request,
          "GET",
          "/v1/settings/model-policy",
        );
        const savedCoordinator = readyPolicyCoordinator(saved, providers);
        if (savedCoordinator) {
          return {
            providerId: savedCoordinator.provider_id,
            model: savedCoordinator.model,
          };
        }
        diagnostic = `policy write did not stick for ${providerId}/${model}`;
      } catch (error) {
        diagnostic = `${providerId}/${model}: ${error instanceof Error ? error.message : String(error)}`;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, Math.min(1000, 250 * (attempt + 1))));
  }
  throw new Error(`harness could not establish a default model: ${diagnostic}`);
}

/** Clears the global model policy. */
export async function apiClearDefaultModel(
  request: APIRequestContext,
): Promise<void> {
  const empty: ModelPolicy = {
    coordinator: { provider_id: "", model: "" },
    lite: { provider_id: "", model: "" },
    agent_pool: { selection: "first", models: [] },
  };
  await apiJson(request, "PATCH", "/v1/settings/model-policy", empty);
}

export async function apiFindHarnessProject(request: APIRequestContext) {
  const projects = await apiListProjects(request);
  const project = projects.find((p) => (p.name ?? "").toLowerCase().includes("harness"));
  expect(project?.id).toBeTruthy();
  return project!;
}

export async function apiGetActiveWorkflowRun(
  request: APIRequestContext,
  sessionId: string,
): Promise<WorkflowRun> {
  const run = await apiTryGetActiveWorkflowRun(request, sessionId);
  if (!run) throw new Error(`chat ${sessionId} has no active workflow run`);
  return run;
}

export async function apiTryGetActiveWorkflowRun(
  request: APIRequestContext,
  sessionId: string,
): Promise<WorkflowRun | null> {
  const { run } = await apiJson<ActiveWorkflowRunResponse>(
    request,
    "GET",
    `/v1/sessions/${sessionId}/workflow-runs/active`,
  );
  return run ?? null;
}

export async function apiGetWorkflowRun(
  request: APIRequestContext,
  runId: string,
): Promise<WorkflowRun> {
  return apiJson<WorkflowRun>(request, "GET", `/v1/workflow-runs/${runId}`);
}

export async function apiStartWorkflowRun(
  request: APIRequestContext,
  sessionId: string,
  body: Omit<StartWorkflowRunRequest, "operation_id">,
): Promise<WorkflowRun> {
  await waitForSessionReady(request, sessionId);
  return apiJson<WorkflowRun>(request, "POST", `/v1/sessions/${sessionId}/workflow-runs`, {
    operation_id: crypto.randomUUID(),
    ...body,
  } satisfies StartWorkflowRunRequest);
}

export async function apiApprovePlan(
  request: APIRequestContext,
  projectId: string,
  blueprintPath: string,
  runId: string,
): Promise<void> {
  const [run, blueprint] = await Promise.all([
    apiGetWorkflowRun(request, runId),
    apiGetBlueprint(request, projectId, blueprintPath),
  ]);
  await apiJson(
    request,
    "POST",
    `/v1/projects/${projectId}/blueprints/${blueprint.id}/approve`,
    {
      workflow_run_id: run.id,
      expected_revision: run.revision,
      content_digest: createHash("sha256").update(blueprint.content).digest("hex"),
    },
  );
}

export async function apiFireWorkflowTransition(
  request: APIRequestContext,
  runId: string,
  transitionId: string,
): Promise<WorkflowRun> {
  const current = await apiGetWorkflowRun(request, runId);
  return apiJson<WorkflowRun>(
    request,
    "POST",
    `/v1/workflow-runs/${runId}/transitions/${encodeURIComponent(transitionId)}`,
    { expected_revision: current.revision },
  );
}

/** Specs follow runs by blueprint path; the host addresses blueprints by id. */
async function apiBlueprintIdAtPath(
  request: APIRequestContext,
  projectId: string,
  blueprintPath: string,
): Promise<string> {
  const { blueprints } = await apiJson<BlueprintListResponse>(
    request,
    "GET",
    `/v1/projects/${projectId}/blueprints?path=${encodeURIComponent(blueprintPath)}`,
  );
  const [summary] = blueprints;
  if (!summary) throw new Error(`no blueprint at ${blueprintPath}`);
  return summary.id;
}

export async function apiUpdatePlanContent(
  request: APIRequestContext,
  projectId: string,
  blueprintPath: string,
  content: string,
): Promise<void> {
  const id = await apiBlueprintIdAtPath(request, projectId, blueprintPath);
  await apiJson(request, "PATCH", `/v1/projects/${projectId}/blueprints/${id}`, { content });
}

export async function apiCreateBlueprint(
  request: APIRequestContext,
  projectId: string,
  body: CreateBlueprintRequest,
): Promise<Blueprint> {
  return apiJson<Blueprint>(request, "POST", `/v1/projects/${projectId}/blueprints`, body);
}

export async function apiGetBlueprint(
  request: APIRequestContext,
  projectId: string,
  blueprintPath: string,
): Promise<Blueprint> {
  const id = await apiBlueprintIdAtPath(request, projectId, blueprintPath);
  return apiJson<Blueprint>(request, "GET", `/v1/projects/${projectId}/blueprints/${id}`);
}

/** Capture session_id from sidecar JSON responses during a harness flow. */
export function captureWireSessionId(page: Page): { get: () => string | undefined } {
  let sessionId: string | undefined;
  page.on("response", async (resp) => {
    if (!resp.ok()) return;
    const url = resp.url();
    if (!url.includes("/v1/")) return;
    try {
      const body = (await resp.json()) as { session_id?: string; id?: string };
      if (typeof body.session_id === "string" && body.session_id) {
        sessionId = body.session_id;
      }
      if (resp.request().method() === "POST" && url.endsWith("/v1/sessions") && typeof body.id === "string") {
        sessionId = body.id;
      }
    } catch {
      // Sidecar responses can have non-JSON bodies.
    }
  });
  return { get: () => sessionId };
}
