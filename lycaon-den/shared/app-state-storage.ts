import { CONFIG_DIR_NAME } from "./brand.ts";
import { APP_STATE_VERSION } from "./app-state-types.ts";

export const APP_STATE_STORAGE_KEY = `${CONFIG_DIR_NAME}.app-state`;
export const APP_STATE_STORAGE_SLICE_PREFIX = `${APP_STATE_STORAGE_KEY}.v${APP_STATE_VERSION}.`;

export function appStateStorageSliceKey(slice: string): string {
  return `${APP_STATE_STORAGE_SLICE_PREFIX}${encodeURIComponent(slice)}`;
}
