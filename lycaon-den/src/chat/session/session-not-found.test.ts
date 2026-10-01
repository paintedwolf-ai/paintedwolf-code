import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { isScanNotFoundError, isSessionNotFoundError } from "./session-not-found.ts";

describe("isSessionNotFoundError", () => {
  it("matches the host's missing-chat code", () => {
    expect(
      isSessionNotFoundError(new LycaonApiError("missing", 404, "session_not_found")),
    ).toBe(true);
  });

  it("does not read another missing subject as a missing chat", () => {
    expect(
      isSessionNotFoundError(new LycaonApiError("File edit not found.", 404, "not_found")),
    ).toBe(false);
    expect(
      isSessionNotFoundError(new LycaonApiError("bad", 500, "internal_error")),
    ).toBe(false);
  });
});

describe("isScanNotFoundError", () => {
  it("matches only the missing-scan code", () => {
    expect(isScanNotFoundError(new LycaonApiError("gone", 404, "scan_not_found"))).toBe(true);
    expect(isScanNotFoundError(new LycaonApiError("gone", 404, "not_found"))).toBe(false);
  });
});
