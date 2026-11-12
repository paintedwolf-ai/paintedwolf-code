import { describe, expect, it } from "vitest";
import { editorOnlyReason } from "./root-admission.ts";

describe("editorOnlyReason", () => {
  it("is empty for an admitted root", () => {
    expect(editorOnlyReason({ admitted: true, admission_code: null })).toBeNull();
  });

  it("explains each class the host reports", () => {
    const codes = ["write_root_contains_control_plane", "write_root_under_secret_store"] as const;
    const reasons = codes.map((admission_code) => editorOnlyReason({ admitted: false, admission_code }));
    expect(reasons.every((reason) => typeof reason === "string" && reason.length > 0)).toBe(true);
    expect(new Set(reasons).size).toBe(codes.length);
  });
});
