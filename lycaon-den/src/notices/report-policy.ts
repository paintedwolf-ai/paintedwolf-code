import { LycaonApiError } from "../api/http.ts";
import { ClientNoticeError } from "./client-notices.ts";
import { BackendTransportError } from "../platform/connection/request-connectivity.ts";

/** Connectivity belongs to the stop screen; admission refusals are the transport's to absorb. */
export function shouldReportToNoticeRail(err: unknown): boolean {
  if (err instanceof LycaonApiError) {
    return (
      err.code !== "rate_limited" &&
      err.code !== "source_view_not_found" &&
      err.code !== "source_workspace_mismatch"
    );
  }
  if (err instanceof BackendTransportError) {
    return err.reachability === "reachable";
  }
  if (err instanceof ClientNoticeError) {
    return err.kind !== "offline" && err.kind !== "fetch_failure";
  }
  // Unstamped TypeErrors have no safe display copy.
  return !(err instanceof TypeError);
}
