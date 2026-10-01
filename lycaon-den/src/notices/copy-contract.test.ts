import { describe, expect, it } from "vitest";
import { loadSourceCorpus } from "../test/source-corpus.ts";

const notices = loadSourceCorpus(import.meta.dirname, { extensions: [".ts"] });

/** User-facing title strings allowed in hand-written modules (wire fallbacks only). */
const NOTICE_MODEL_ALLOWLIST = [
  "Could not complete request",
  "The last action did not complete, and there is no more detail for this one.",
];

/**
 * The one file allowed to carry Den's own notice copy.
 *
 * Copy lives in `config/packs/painted-wolf/platform/host/client-notices/` and
 * arrives here through `./task codegen:client-notices`, so the only exception is
 * the generated output. No hand-written module in Den may declare notice copy.
 */
const GENERATED_CLIENT_NOTICES = "client-notices.generated.ts";

describe("notices copy contract", () => {
  it("keeps notice copy out of hand-written Den modules", () => {
    const files = notices.files.filter(
      (file) =>
        file.rel !== GENERATED_CLIENT_NOTICES &&
        !file.rel.endsWith(".test.ts"),
    );
    const titlePattern = /\btitle:\s*["'`][^"'`]+["'`]/g;
    for (const file of files) {
      const matches = file.text.match(titlePattern) ?? [];
      for (const match of matches) {
        const allowed = NOTICE_MODEL_ALLOWLIST.some((snippet) => match.includes(snippet));
        expect(allowed, `${file.rel} must not define notice title copy (${match})`).toBe(true);
      }
    }
  });

  it("does not import generated or server notice registries", () => {
    const files = notices.files.filter((file) => !file.rel.endsWith(".test.ts"));
    for (const file of files) {
      expect(file.text).not.toMatch(/notice-copy\.generated/);
      expect(file.text).not.toMatch(/from\s+["'].*notice-copy/);
    }
  });
});
