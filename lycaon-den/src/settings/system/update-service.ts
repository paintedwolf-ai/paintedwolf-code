import type { UpdateError } from "./update-error.ts";
import { invoke } from "@tauri-apps/api/core";
import { listenHostEvent } from "../../platform/windows/window-channel.ts";
const UPDATE_STATE_EVENT = "update-state-changed";
type InstallSource = "homebrew_cask" | "direct_download" | "system_package" | "unpackaged" | "unknown";
