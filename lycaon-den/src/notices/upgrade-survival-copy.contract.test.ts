import { describe, expect, it } from "vitest";
import { denSourceRoot, loadTypeScriptCorpus, requireNonEmpty } from "../test/source-corpus.ts";
import { CLIENT_NOTICES } from "./client-notices.generated.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const sources = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true });

/** Catalog fragments that must never appear as string literals in Den UI sources. */
const recoveryCopyFragments = [
  CLIENT_NOTICES.store_incompatible_integrity.message.split(".")[0]!,
  CLIENT_NOTICES.store_incompatible_integrity_no_snapshot.message.split(".")[0]!,
  CLIENT_NOTICES.store_incompatible_schema.message.split(".")[0]!,
  CLIENT_NOTICES.store_incompatible_schema_no_snapshot.message.split(".")[0]!,
  CLIENT_NOTICES.store_incompatible_restore_failed.title,
  CLIENT_NOTICES.store_incompatible_snapshot_incompatible.title,
  CLIENT_NOTICES.store_incompatible_restart.title,
  "Your data needs attention",
  "Finish restoring",
] as const;

describe("upgrade survival recovery copy", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  it("resolves every store-incompatible notice through CLIENT_NOTICES", () => {
    for (const key of [
      "store_incompatible_integrity",
      "store_incompatible_integrity_no_snapshot",
      "store_incompatible_schema",
      "store_incompatible_schema_no_snapshot",
      "store_incompatible_restore_failed",
      "store_incompatible_snapshot_incompatible",
      "store_incompatible_restart",
    ] as const) {
      expect(CLIENT_NOTICES[key].title.length).toBeGreaterThan(0);
      expect(CLIENT_NOTICES[key].message.length).toBeGreaterThan(0);
    }
  });

  it("keeps recovery copy out of critical-stop-model and components", () => {
    const criticalStop = sources.file("notices/critical-stop-model.ts");
    expect(criticalStop).toBeDefined();
    const targets = requireNonEmpty("upgrade survival copy", [
      ...(criticalStop ? [criticalStop] : []),
      ...sources.under("components"),
      ...sources.under("files"),
    ]).filter((file) => file.rel !== "notices/client-notices.generated.ts");
    for (const file of targets) {
      for (const fragment of recoveryCopyFragments) {
        if (!fragment || fragment.length < 8) continue;
        expect(file.text, `${file.rel} must not embed ${JSON.stringify(fragment)}`).not.toContain(
          fragment,
        );
      }
    }
  });
});
