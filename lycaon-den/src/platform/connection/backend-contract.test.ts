// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../..");

describe("token handoff contract", () => {
  it("app-state types must not include api token fields", () => {
    const shared = readFileSync(
      join(root, "shared/app-state-types.ts"),
      "utf8",
    );
    expect(shared).not.toMatch(/api_token|apiToken/i);
  });

  it("platform backend module must not persist token to storage APIs", () => {
    const backend = readFileSync(
      join(root, "src/platform/connection/backend.ts"),
      "utf8",
    );
    expect(backend).not.toMatch(/localStorage|write_app_state/);
    expect(backend).toMatch(/cachedConnection/);
  });
});
