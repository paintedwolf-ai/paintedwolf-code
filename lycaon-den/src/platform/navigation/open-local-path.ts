import { browserDestinationLabel, browserDestinationUnavailable, DEFAULT_APPLICATION_LABEL } from "../desktop/open-browser.ts";
import localFileFormats from "../../../shared/local-file-formats.json";
import { externalOpenPrefs, EDITOR_LABELS } from "../../settings/editor/external-open-prefs.ts";
import { hostSharesDevice } from "../connection/host-identity.ts";
import { isTauriRuntime, tauriPlatform } from "../runtime.ts";
import { openInExternalEditor } from "./external-source.ts";
import { pathIsUnderProjectRoots, revealInFileManager } from "../files/reveal-in-file-manager.ts";

/** A current filesystem location, never a historical source version. */
export type LocalPathTarget = {
  absolutePath: string;
  /** Device exports are local even when the connected host is remote. */
  origin?: "host" | "device";
  projectRoots: readonly string[];
  entryKind: "file" | "folder";
  line?: number;
  unavailable?: string;
};
export type LocalPathDestination = "file-manager" | "editor" | "browser" | "default-application";

export function browserCanOpenPath(path: string): boolean {
  const extension = path.split(/[\\/]/).pop()?.split(".").slice(1).pop()?.toLowerCase();
  return extension != null && localFileFormats.browser.includes(extension);
}

export function localPathDestinations(target: LocalPathTarget): LocalPathDestination[] {
  return ["file-manager", "editor", ...(target.entryKind === "file"
    ? [...(browserCanOpenPath(target.absolutePath) ? ["browser" as const] : []), "default-application" as const]
    : [])];
}

export function localPathOnDevice(target: LocalPathTarget): boolean {
  return target.origin === "device" || hostSharesDevice();
}

export function localPathUnavailable(target: LocalPathTarget): string | undefined {
  if (target.unavailable) return target.unavailable;
  if (!localPathOnDevice(target)) return "This path is on another device.";
  if (!isTauriRuntime()) return "Open this path from the desktop app.";
  if (!pathIsUnderProjectRoots(target.absolutePath, target.projectRoots)) return "This path is outside the available project folders.";
  return undefined;
}

export function localPathDestinationUnavailable(target: LocalPathTarget, destination: LocalPathDestination): string | undefined {
  const unavailable = localPathUnavailable(target);
  if (unavailable) return unavailable;
  const prefs = externalOpenPrefs();
  if (destination === "editor" && prefs.externalEditor === "custom") {
    const fileCommand = prefs.customOpenCommand?.trim() ?? "";
    const folderCommand = prefs.customOpenFolderCommand?.trim() ?? "";
    if (target.entryKind === "folder" && !folderCommand && fileCommand.includes("{line}")) {
      return "Set a folder command in editor settings to open folders with this editor.";
    }
    if (!(target.entryKind === "folder" ? folderCommand || fileCommand : fileCommand)) return "Set a custom editor command in settings.";
  }
  if (destination === "browser") return browserDestinationUnavailable(prefs);
  return undefined;
}

export function localPathDestinationLabel(destination: LocalPathDestination): string {
  const prefs = externalOpenPrefs();
  switch (destination) {
    case "file-manager": {
      const platform = tauriPlatform();
      return platform === "windows" ? "File Explorer" : platform === "linux" ? "Files" : platform === "macos" ? "Finder" : "File manager";
    }
    case "editor": return prefs.externalEditor === "custom" ? "External editor" : EDITOR_LABELS[prefs.externalEditor];
    case "browser": return browserDestinationLabel(prefs.browser);
    case "default-application": return DEFAULT_APPLICATION_LABEL;
  }
}

export async function openLocalPath(target: LocalPathTarget, destination: LocalPathDestination): Promise<void> {
  const unavailable = localPathDestinationUnavailable(target, destination);
  if (unavailable) throw new Error(unavailable);
  if (!localPathDestinations(target).includes(destination)) throw new Error("This destination cannot open this kind of item.");
  const prefs = externalOpenPrefs();
  if (destination === "file-manager") {
    const result = await revealInFileManager(target.absolutePath, target.projectRoots);
    if (result.status !== "revealed") throw new Error("The file manager could not reveal this item.");
  } else if (destination === "editor") {
    const result = await openInExternalEditor({ ...target, preset: prefs.externalEditor, customTemplate: prefs.customOpenCommand, customFolderTemplate: prefs.customOpenFolderCommand });
    if (result.status !== "opened") throw new Error(result.reason);
  } else {
    const { invoke } = await import("@tauri-apps/api/core");
    await invoke("open_local_path", { absolutePath: target.absolutePath, projectRoots: [...target.projectRoots], destination,
      browser: prefs.browser, customBrowserCommand: prefs.customBrowserCommand ?? null });
  }
}
