import { existsSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { NO_PROVIDER_PREFLIGHT_CODE } from "./no-provider-card.ts";

const REPO_ROOT = join(import.meta.dirname, "../../..");
const NO_PROVIDER_YAML = join(
  REPO_ROOT,
  "lycaon/config/packs/painted-wolf/platform/host/user-notices",
  `${NO_PROVIDER_PREFLIGHT_CODE}.yaml`,
);

describe("missing-provider notice", () => {
  it("has a host notice for the code Den stands down", () => {
    expect(
      existsSync(NO_PROVIDER_YAML),
      `PreflightNudge suppresses ${NO_PROVIDER_PREFLIGHT_CODE} on Home while the card is up; ` +
        `renaming the catalog file leaves the card and the readiness nudge both reporting it`,
    ).toBe(true);
  });
});
