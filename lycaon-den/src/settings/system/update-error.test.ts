import { describe, expect, it } from "vitest";
import { updateError, UPDATE_ERROR_MESSAGES } from "./update-error.ts";

describe("native update errors", () => {
  it("classifies structured failures without interpreting diagnostic prose", () => {
    expect(updateError({ code: "verification_failed", detail: "network unavailable" })).toEqual({
      code: "verification_failed", detail: "network unavailable",
    });
    expect(updateError(new Error("verification_failed"))).toEqual({
      code: "service_unavailable", detail: "verification_failed",
    });
    expect(updateError({ code: "toString" }).code).toBe("service_unavailable");
    expect(updateError({ code: "future_error", detail: "install_failed" }).code).toBe("service_unavailable");
  });

  it("does not promise an unchanged installation after apply or interruption errors", () => {
    expect(UPDATE_ERROR_MESSAGES.install_failed).not.toMatch(/unaffected|unchanged|not installed/i);
    expect(UPDATE_ERROR_MESSAGES.interrupted).not.toMatch(/unaffected|unchanged|not installed/i);
    expect(UPDATE_ERROR_MESSAGES.verification_failed).toContain("was not installed");
  });
});
