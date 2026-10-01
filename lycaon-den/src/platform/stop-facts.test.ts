import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HealthResponse } from "./connection/backend.ts";
import { resetStoreRevisionTracking } from "./connection/health.ts";
import { stopFactsLine } from "./stop-facts.ts";

vi.mock("./desktop/den-version.ts", () => ({ DEN_VERSION: "1.0.0" }));
vi.mock("./runtime.ts", () => ({ tauriPlatform: () => "macos" }));

const schemaMismatch: HealthResponse = {
  status: "recovery",
  version: "1.0.0",
  store_revision: 1,
  schema_version: 1,
  store_schema_version: 5,
  recovery_reason: "schema_mismatch",
  recovery_snapshot_available: false,
};

describe("stopFactsLine", () => {
  beforeEach(resetStoreRevisionTracking);

  it("distinguishes the expected schema from the refused store schema", () => {
    expect(stopFactsLine(undefined, schemaMismatch)).toBe(
      "engine 1.0.0 · app 1.0.0 · expected schema 1 · found schema 5 · macos",
    );
  });

  it("reports one store schema when the store matches the baseline", () => {
    expect(
      stopFactsLine(undefined, {
        ...schemaMismatch,
        recovery_reason: "integrity_failed",
        store_schema_version: 1,
      }),
    ).toContain("store schema 1");
  });

  it("prefers structured catastrophic facts when supplied", () => {
    expect(
      stopFactsLine({
        app_version: "2.0.0",
        schema_version: 7,
        os_name: "linux",
        os_version: "9",
      }),
    ).toBe("engine 2.0.0 · app 1.0.0 · store schema 7 · linux 9");
  });
});
