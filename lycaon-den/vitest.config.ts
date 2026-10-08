import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import solid from "vite-plugin-solid";
import {
  VITEST_DEFAULT_TIMEOUT_MS,
  VITEST_HOOK_TIMEOUT_MS,
} from "./src/test/vitest-timeouts.ts";

const includePerf = process.env.LYCAON_VITEST_PERF === "1";
const vitestFast = process.env.LYCAON_VITEST_FAST === "1";
// Changed-coverage runs measure only tests related to changed sources, whatever else changed.
const changedOnly = process.env.PW_VITEST_CHANGED_ONLY === "1";
const denRoot = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(denRoot, "..");

function maxWorkers(): number {
  const workers = Number(process.env.PW_VITEST_MAX_WORKERS);
  if (!Number.isInteger(workers) || workers < 1) {
    throw new Error("Verification admission must set PW_VITEST_MAX_WORKERS to a positive integer");
  }
  return workers;
}

const VITEST_FAST_INCLUDE = [
  "src/api/events.test.ts",
  "src/store/app-state.test.ts",
  "src/store/app-state.activity.test.ts",
  "src/store/app-state.transcript.test.ts",
  "src/store/app-state.workers.test.ts",
  "src/store/app-state.board.test.ts",
  "src/chat/send/pending-sends.test.ts",
  "src/chat/transcript/projection/transcript-items.test.ts",
  "src/chat/transcript/projection/transcript-drafts.test.ts",
  "src/chat/transcript/projection/transcript-workflow-feedback.test.ts",
  "src/chat/transcript/projection/transcript-tool-results.test.ts",
  "src/chat/transcript/projection/transcript-message-visibility.test.ts",
  "src/chat/transcript/projection/transcript-tool-visibility.test.ts",
  "src/chat/transcript/projection/transcript-ordering.test.ts",
  "src/chat/transcript/projection/transcript-progress.test.ts",
  "src/chat/transcript/projection/transcript-incremental.test.ts",
  "src/chat/workflow/workflow-spans.test.ts",
  "src/search/search-query-model.test.ts",
  "src/settings/providers/models-editor-model.test.ts",
  "src/App.boot.test.tsx",
  "src/components/transcript/pending-transcript.test.tsx",
] as const;

function projectInclude(kind: "ts" | "tsx"): string[] {
  if (vitestFast) {
    return VITEST_FAST_INCLUDE.filter((p) => p.endsWith(`.${kind}`));
  }
  return [`src/**/*.test.${kind}`];
}

function projectExclude(kind: "ts" | "tsx"): string[] {
  const extra: string[] = [];
  if (!includePerf) extra.push(`src/**/*.perf.test.${kind}`);
  return extra;
}

export default defineConfig({
  plugins: [solid()],
  server: {
    fs: {
      allow: [denRoot, repoRoot],
    },
  },
  test: {
    globals: true,
    maxWorkers: maxWorkers(),
    ...(changedOnly ? { forceRerunTriggers: [] } : {}),
    testTimeout: VITEST_DEFAULT_TIMEOUT_MS,
    hookTimeout: VITEST_HOOK_TIMEOUT_MS,
    setupFiles: ["./vitest.setup.ts"],
    projects: [
      {
        extends: true,
        test: {
          name: "model",
          environment: "node",
          include: projectInclude("ts"),
          exclude: projectExclude("ts"),
        },
      },
      {
        extends: true,
        test: {
          name: "dom",
          environment: "jsdom",
          include: projectInclude("tsx"),
          exclude: projectExclude("tsx"),
        },
      },
    ],
    coverage: {
      provider: "v8",
      reporter: ["text", "json-summary"],
      include: [
        "src/api/**/*.ts",
        "src/store/**/*.ts",
        "src/settings/**/*.ts",
        "src/workflow/**/*.ts",
        "src/chat/tool/tool-part-model.ts",
        "src/chat/workflow/workflow-spans.ts",
        "src/chat/composer/composer-rules.ts",
        "src/chat/transcript/projection/transcript-items.ts",
        "src/chat/transcript/projection/transcript-item-order.ts",
        "src/chat/transcript/projection/transcript-display-projection.ts",
        "src/platform/connection/backend.ts",
        "src/platform/persistence/app-state.ts",
        "src/platform/persistence/app-state-parse.ts",
      ],
      exclude: [
        "src/**/*.test.ts",
        "src/api/mocks/**",
      ],
      // Floors live in scripts/coverage-policy.json and are enforced by
      // den:coverage-check and den:coverage:changes.
    },
  },
});
