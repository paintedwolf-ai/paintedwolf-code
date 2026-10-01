/** Native editor adapter. Destination policy lives in open-local-path.ts. */

import type { ExternalEditorPreset } from "../../../shared/app-state-types.ts";
import { isTauriRuntime } from "../runtime.ts";

export type OpenInExternalEditorArgs = {
  absolutePath: string;
  line?: number;
  projectRoots: readonly string[];
  preset?: ExternalEditorPreset;
  customTemplate?: string;
  customFolderTemplate?: string;
};

export type OpenInExternalEditorResult =
  | { status: "opened" }
  | { status: "rejected"; reason: string };

export type OpenInExternalEditorOptions = {
  isTauri?: boolean;
  invokeOpen?: (args: {
    absPath: string;
    line?: number;
    preset: string;
    customTemplate?: string;
    customFolderTemplate?: string;
    projectRoots: string[];
  }) => Promise<void>;
};

async function defaultInvoke(args: {
  absPath: string;
  line?: number;
  preset: string;
  customTemplate?: string;
  customFolderTemplate?: string;
  projectRoots: string[];
}): Promise<void> {
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("open_in_editor", {
    absPath: args.absPath,
    line: args.line ?? null,
    preset: args.preset,
    customTemplate: args.customTemplate ?? null,
    customFolderTemplate: args.customFolderTemplate ?? null,
    projectRoots: args.projectRoots,
  });
}

export async function openInExternalEditor(
  args: OpenInExternalEditorArgs,
  options?: OpenInExternalEditorOptions,
): Promise<OpenInExternalEditorResult> {
  const preset = args.preset ?? "cursor";
  const tauri =
    options?.isTauri !== undefined ? options.isTauri : isTauriRuntime();

  if (!tauri) {
    return { status: "rejected", reason: "Open this path from the desktop app." };
  }

  try {
    const invoke = options?.invokeOpen ?? defaultInvoke;
    await invoke({
      absPath: args.absolutePath,
      line: args.line,
      preset,
      customTemplate: args.customTemplate,
      customFolderTemplate: args.customFolderTemplate,
      projectRoots: [...args.projectRoots],
    });
    return { status: "opened" };
  } catch (e) {
    return {
      status: "rejected",
      reason: e instanceof Error ? e.message : String(e),
    };
  }
}
