import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { MODELS_SETTINGS_COPY } from "../settings/providers/models-settings-copy.ts";
import { NO_PROVIDER_PREFLIGHT_CODE } from "./no-provider-card.ts";

/**
 * The missing-provider condition has two copy sources: the Den card (`NoProviderCard`)
 * and the host readiness notice (`user-notices/`). They never render together
 * (Home stands the host code down via `statedElsewhere`), so the sentences differ,
 * but neither may claim how long setup takes.
 */

const REPO_ROOT = join(import.meta.dirname, "../../..");
const NOTICES_DIR = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/host/user-notices",
);
const NO_PROVIDER_YAML = join(NOTICES_DIR, `${NO_PROVIDER_PREFLIGHT_CODE}.yaml`);

/** One notice field, inline or folded (`>-`). Den has no YAML dependency. */
function noticeField(text: string, key: string): string {
  // Folded first: `message: >-` also matches the inline pattern, with `>-` as the value.
  const folded = text.match(
    new RegExp(`^${key}:[ \\t]*[>|][-+]?[ \\t]*\\n((?:[ \\t]+\\S.*\\n?)+)`, "m"),
  );
  if (folded) {
    return folded[1]!
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean)
      .join(" ");
  }
  const inline = text.match(new RegExp(`^${key}:[ \\t]+(\\S.*)$`, "m"));
  if (!inline) throw new Error(`${key} not found in ${NO_PROVIDER_YAML}`);
  return inline[1]!.trim();
}

/** A setup duration: a claim about the provider's signup flow, which neither surface sees. */
const DURATION_CLAIM = /\b(minutes?|moments?|seconds?|instantly)\b/i;

describe("missing-provider copy contract", () => {
  const yaml = () => readFileSync(NO_PROVIDER_YAML, "utf8");

  it("has a host notice for the code Den stands down", () => {
    expect(
      existsSync(NO_PROVIDER_YAML),
      `PreflightNudge suppresses ${NO_PROVIDER_PREFLIGHT_CODE} on Home while the card is up; ` +
        `renaming the catalog file leaves the card and the readiness nudge both reporting it`,
    ).toBe(true);
  });

  // The assertions below are only meaningful when the parser returns whole sentences.
  it("reads whole sentences out of the catalog", () => {
    for (const key of ["title", "message", "suggested_action"]) {
      const value = noticeField(yaml(), key);
      expect(value, `${key} looks like YAML syntax, not copy`).not.toMatch(/^[>|][-+]?$/);
      expect(value.split(/\s+/).length, `${key} parsed as a fragment (${value})`).toBeGreaterThan(2);
    }
  });

  it("states no setup duration on either surface", () => {
    const strings = [
      ["noProviderTitle", MODELS_SETTINGS_COPY.noProviderTitle],
      ["noProviderBody", MODELS_SETTINGS_COPY.noProviderBody],
      ["noProviderCta", MODELS_SETTINGS_COPY.noProviderCta],
      ["noDefaultModelTitle", MODELS_SETTINGS_COPY.noDefaultModelTitle],
      ["noDefaultModelBody", MODELS_SETTINGS_COPY.noDefaultModelBody],
      ["noDefaultModelCta", MODELS_SETTINGS_COPY.noDefaultModelCta],
      ["catalog title", noticeField(yaml(), "title")],
      ["catalog message", noticeField(yaml(), "message")],
      ["catalog suggested_action", noticeField(yaml(), "suggested_action")],
    ] as const;
    for (const [label, copy] of strings) {
      expect(
        DURATION_CLAIM.test(copy),
        `${label} estimates how long connecting a provider takes (${copy})`,
      ).toBe(false);
    }
  });

  it("routes both surfaces to the same remedy", () => {
    for (const [label, copy] of [
      ["noProviderCta", MODELS_SETTINGS_COPY.noProviderCta],
      ["catalog suggested_action", noticeField(yaml(), "suggested_action")],
    ] as const) {
      expect(copy, `${label} must tell the user to add a provider`).toMatch(/add/i);
      expect(copy, `${label} must name the provider`).toMatch(/provider/i);
    }
  });
});
