import { LycaonApiError } from "../../api/http.ts";

/** The host no longer has this chat. Other 404s name something inside it. */
export function isSessionNotFoundError(err: unknown): boolean {
  return err instanceof LycaonApiError && err.code === "session_not_found";
}

export function isScanNotFoundError(err: unknown): boolean {
  return err instanceof LycaonApiError && err.code === "scan_not_found";
}
