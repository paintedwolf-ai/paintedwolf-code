import path from "node:path";

/** Mutable frontend files follow the same lifetime as their harness state. */
export function harnessRuntimePaths(stateDirectory: string | undefined): {
  cacheDir?: string;
  scrollDebugFile?: string;
} {
  if (!stateDirectory?.trim()) return {};
  const root = path.resolve(stateDirectory);
  return {
    cacheDir: path.join(root, "runtime", "vite-cache"),
    scrollDebugFile: path.join(root, "config", "debug", "den-scroll.jsonl"),
  };
}
