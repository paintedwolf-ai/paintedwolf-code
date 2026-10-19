import type { SourceWatchCoverage } from "../../api/types.ts";

/** Host coverage explains which outside changes may be absent from this view. */
export function sourceWatchNotice(coverage: SourceWatchCoverage | undefined): string | null {
  if (!coverage) return null;
  if (coverage.state === "live") {
    return coverage.policy_unwatched ? "Some folders are refreshed on demand" : null;
  }
  if (coverage.watch_limit_reached) {
    return "Linux reached its file watch limit. Outside changes may be missing; free watches or ask an administrator to raise fs.inotify.max_user_watches, then reopen the file.";
  }
  switch (coverage.state) {
    case "partial":
      return "Outside changes in some folders are not watched";
    case "faulted":
      return "Watching for outside changes stopped; reopen the file to refresh";
    case "unwatched":
      return "Outside changes to this file are not being watched yet";
  }
}
