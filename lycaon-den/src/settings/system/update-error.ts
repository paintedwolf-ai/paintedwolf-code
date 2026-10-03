/** Recovery guidance keyed by native failure codes. */
export const UPDATE_ERROR_MESSAGES = {
  state_unavailable: "Update settings could not be opened. Restart the app and try again.",
  invalid_version: "This app has invalid version information. Download a fresh copy to update.",
  invalid_transition: "Another update action is in progress. Wait for it to finish and try again.",
  candidate_missing: "Check for updates again before installing.",
  candidate_changed: "The available update changed. Review it before installing.",
  package_managed: "Homebrew manages this installation. Use its upgrade command to update.",
  install_source_unavailable: "The app could not verify how it was installed. Reinstall it using the same method, then try again.",
  preferences_unavailable: "Update preferences could not be read or saved. Check storage permissions and available space, then try again.",
  journal_unavailable: "Update recovery information could not be read or saved. Check storage permissions and available space, then try again.",
  interrupted: "The update was interrupted. Check for updates to try again.",
  check_failed: "Could not check for updates. Check your connection and try again. Your installed version is unaffected.",
  download_failed: "The update could not be downloaded. Check your connection and try again. Your installed version is unaffected.",
  verification_failed: "The update could not be verified and was not installed. Check for updates again. Your installed version is unaffected.",
  install_failed: "The update could not be installed. Check available space and app permissions, then try again. If the app cannot reopen, download a fresh copy.",
  invalid_release: "The update information is not valid for this app. Check for updates again later.",
  cancelled: "The update was cancelled. Your installed version is unaffected.",
  disk_space: "There is not enough free space to prepare this update.",
  unsupported_installation: "This installation needs permission or a supported location to update.",
  engine_stop_failed: "The engine did not stop cleanly. The update has been deferred.",
  release_withdrawn: "This update is no longer offered. Check for updates again.",
  activation_failed: "The update could not be activated. Retry the update or download a fresh copy.",
  service_unavailable: "The updater could not complete this action. Try again, or restart the app if the problem continues.",
} as const;

export type UpdateErrorCode = keyof typeof UPDATE_ERROR_MESSAGES;
export type UpdateError = { code: UpdateErrorCode; detail?: string };

function isUpdateErrorCode(value: string): value is UpdateErrorCode {
  return Object.hasOwn(UPDATE_ERROR_MESSAGES, value);
}

export function updateError(error: unknown): UpdateError {
  if (typeof error === "object" && error !== null && "code" in error &&
    typeof error.code === "string" && isUpdateErrorCode(error.code)) {
    return {
      code: error.code,
      detail: "detail" in error && typeof error.detail === "string" ? error.detail : undefined,
    };
  }
  return { code: "service_unavailable", detail: error instanceof Error ? error.message : String(error) };
}
