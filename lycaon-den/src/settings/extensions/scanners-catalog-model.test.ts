import { describe, expect, it } from "vitest";
import { scannerJobLabel, scannerJobPhrase } from "./scanners-catalog-model.ts";

describe("scanner job names", () => {
  it("names a slot scanner by its job, not its engine", () => {
    const sca = { id: "lycaon-sca", label: "OSV-Scalibr", categories: ["sca", "security"] };
    expect(scannerJobLabel(sca)).toBe("Dependency scanner");
    expect(scannerJobPhrase(sca)).toBe("the dependency scanner");
    expect(scannerJobLabel({ id: "gitleaks", label: "Gitleaks", categories: ["security", "secret"] })).toBe(
      "Secret scanner",
    );
  });

  it("keeps the catalog label for a scanner outside the three jobs", () => {
    const custom = { id: "license-check", label: "License check", categories: ["license"] };
    expect(scannerJobLabel(custom)).toBe("License check");
    expect(scannerJobPhrase(custom)).toBe("License check");
    expect(scannerJobLabel({ id: "bare" })).toBe("bare");
  });
});
