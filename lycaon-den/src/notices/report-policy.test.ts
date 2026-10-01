import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../api/http.ts";
import { clientNoticeError } from "./client-notices.ts";
import { shouldReportToNoticeRail } from "./report-policy.ts";
import {
  BackendTransportError,
  isBackendUnreachableError,
} from "../platform/connection/request-connectivity.ts";

describe("backend transport classification", () => {
  it("uses the fetch-boundary type, not browser error copy", () => {
    const err = new BackendTransportError(
      new TypeError("localized browser message"),
      "unreachable",
    );
    expect(isBackendUnreachableError(err)).toBe(true);
  });

  it("does not classify an arbitrary TypeError", () => {
    expect(isBackendUnreachableError(new TypeError("Failed to fetch"))).toBe(
      false,
    );
  });

  it("keeps a failed request off the global connectivity path when health answered", () => {
    const err = new BackendTransportError(
      new Error("failure"),
      "reachable",
    );
    expect(isBackendUnreachableError(err)).toBe(false);
  });
});

describe("shouldReportToNoticeRail", () => {
  it("never reports confirmed backend unreachability", () => {
    const err = new BackendTransportError(
      new Error("failure"),
      "unreachable",
    );
    expect(shouldReportToNoticeRail(err)).toBe(false);
  });

  it("reports a request failure when the independent health check answered", () => {
    const err = new BackendTransportError(
      new Error("failure"),
      "reachable",
    );
    expect(shouldReportToNoticeRail(err)).toBe(true);
  });

  it("never reports client connectivity notices", () => {
    expect(shouldReportToNoticeRail(clientNoticeError("offline"))).toBe(false);
    expect(shouldReportToNoticeRail(clientNoticeError("fetch_failure"))).toBe(
      false,
    );
  });

  it("always reports structured API errors", () => {
    expect(
      shouldReportToNoticeRail(new LycaonApiError("missing", 404, "not_found")),
    ).toBe(true);
  });

  it("never reports host admission control, which the transport already waited out", () => {
    expect(
      shouldReportToNoticeRail(new LycaonApiError("Busy", 429, "rate_limited", { retryAfterMs: 1000 })),
    ).toBe(false);
  });

  it("never reports ephemeral source view lifecycle errors", () => {
    expect(
      shouldReportToNoticeRail(new LycaonApiError("Expired", 404, "source_view_not_found")),
    ).toBe(false);
    expect(
      shouldReportToNoticeRail(new LycaonApiError("Mismatch", 409, "source_workspace_mismatch")),
    ).toBe(false);
  });

  it("reports an API error even when it mentions being unavailable", () => {
    expect(
      shouldReportToNoticeRail(
        new LycaonApiError("offline", 500, "internal_error"),
      ),
    ).toBe(true);
  });

  it("reports ordinary client notices", () => {
    expect(
      shouldReportToNoticeRail(clientNoticeError("session_load_failed")),
    ).toBe(true);
  });

  it("does not turn bare TypeErrors into user notices", () => {
    expect(shouldReportToNoticeRail(new TypeError("Load failed"))).toBe(false);
  });
});
