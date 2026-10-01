import { describe, expect, it } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { BackendTransportError } from "../../platform/connection/request-connectivity.ts";
import {
  WORKSPACE_FAULT_GENERIC,
  WORKSPACE_FAULT_IDENTITY_UNSTABLE,
  workspaceFaultMessage,
  workspaceLookupRetryable,
} from "./files-workspace-fault.ts";
import { SourceWorkspaceMismatchError } from "./source-workspace-identity.ts";

describe("workspace lookup fault policy", () => {
  it("retries transport gaps and the structured temporary lifecycle lock", () => {
    expect(
      workspaceLookupRetryable(new BackendTransportError(new TypeError("fetch"), "unreachable")),
    ).toBe(true);
    expect(
      workspaceLookupRetryable(new BackendTransportError(new TypeError("fetch"), "reachable")),
    ).toBe(true);
    expect(workspaceLookupRetryable(new LycaonApiError("gone", 404, "project_not_found"))).toBe(
      false,
    );
    expect(workspaceLookupRetryable(new LycaonApiError("changing", 409, "project_mutation_in_progress"))).toBe(true);
    expect(workspaceLookupRetryable(new LycaonApiError("changing", 409, "project_busy"))).toBe(false);
    expect(workspaceLookupRetryable(new LycaonApiError("changing", 409, "root_busy"))).toBe(false);
    expect(workspaceLookupRetryable(new Error("Unknown API operation GET /v1/x"))).toBe(false);
    expect(workspaceLookupRetryable("string")).toBe(false);
  });

  it("shows the host's copy for a typed refusal and a generic line otherwise", () => {
    expect(
      workspaceFaultMessage(
        new LycaonApiError("This project is not attached any more.", 404, "project_not_found"),
      ),
    ).toBe("This project is not attached any more.");
    expect(workspaceFaultMessage(new LycaonApiError("   ", 500, "internal_error"))).toBe(
      WORKSPACE_FAULT_GENERIC,
    );
    expect(workspaceFaultMessage(new Error("Unknown API operation GET /v1/x"))).toBe(
      WORKSPACE_FAULT_GENERIC,
    );
    expect(workspaceFaultMessage(undefined)).toBe(WORKSPACE_FAULT_GENERIC);
  });

  it("names an identity that would not hold still as its own fault", () => {
    expect(
      workspaceFaultMessage(new SourceWorkspaceMismatchError("workspace-a", "workspace-b")),
    ).toBe(WORKSPACE_FAULT_IDENTITY_UNSTABLE);
    expect(
      workspaceLookupRetryable(new SourceWorkspaceMismatchError("workspace-a", "workspace-b")),
    ).toBe(false);
  });
});
