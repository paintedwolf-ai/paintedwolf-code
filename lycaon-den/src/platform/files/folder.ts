import { nativePathDialog } from "./native-path-dialog.ts";
import { isTauriRuntime } from "../runtime.ts";
import { hostSharesDevice } from "../connection/host-identity.ts";
import { requestHostFolder } from "./host-folder-dialog.ts";

/** Pick a project folder through the native panel, or type a host path. */
export async function pickProjectFolder(): Promise<string | null> {
  if (isTauriRuntime() && hostSharesDevice()) {
    const picked = await nativePathDialog("Open project folder", { kind: "folder" });
    return picked?.path ?? null;
  }
  return requestHostFolder();
}

/** Browser uploads retain archive bytes in a File. */
export function pickBackupArchive(): Promise<File | null> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.accept = ".zip,application/zip";
    input.style.display = "none";
    input.addEventListener("change", () => {
      const file = input.files?.[0] ?? null;
      input.remove();
      resolve(file);
    });
    input.addEventListener("cancel", () => {
      input.remove();
      resolve(null);
    });
    document.body.appendChild(input);
    input.click();
  });
}
