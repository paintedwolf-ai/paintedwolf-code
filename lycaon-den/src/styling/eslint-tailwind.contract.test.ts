import { readSourceText } from "../test/stylesheet-source.ts";

import { join } from "node:path";
import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { ESLint } from "eslint";
import { VITEST_SUBPROCESS_SUITE_TIMEOUT_MS } from "../test/vitest-timeouts.ts";
import { describe, expect, it } from "vitest";

const denRoot = join(dirname(fileURLToPath(import.meta.url)), "../..");
const fixtures = join(denRoot, "src/styling/fixtures");

async function lintFixture(path: string) {
  const eslint = new ESLint({
    cwd: denRoot,
    ignore: false,
  });
  return eslint.lintFiles([path]);
}

describe(
  "eslint-plugin-tailwindcss contract",
  () => {
    it("flags contradicting Tailwind utilities in fixture TSX", async () => {
      const [result] = await lintFixture(join(fixtures, "tailwind-eslint-invalid.tsx"));
      expect(result?.errorCount ?? 0).toBeGreaterThan(0);
      expect(
        result?.messages.some((m) => m.ruleId === "tailwindcss/no-contradicting-classname"),
      ).toBe(true);
    });

    it("accepts valid Tailwind utilities and cn() in fixture TSX", async () => {
      const [result] = await lintFixture(join(fixtures, "tailwind-eslint-valid.tsx"));
      expect(result?.errorCount ?? 0, JSON.stringify(result?.messages ?? [])).toBe(0);
    });

    it("den:lint config resolves tailwind.css from repo root", () => {
      const eslint = readSourceText(join(denRoot, "eslint.config.js"), "utf8");
      expect(eslint).toMatch(/cssConfigPath:\s*"\.\/src\/tailwind\.css"/);
      expect(eslint).toMatch(/functions:\s*\[\s*"cn"\s*\]/);
      expect(eslint).toMatch(
        /no-contradicting-classname|"tailwindcss\/no-contradicting-classname":\s*"error"/,
      );
    });
  },
  VITEST_SUBPROCESS_SUITE_TIMEOUT_MS,
);
