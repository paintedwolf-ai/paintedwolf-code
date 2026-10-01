import type { CodeScanEvent, CodeScan } from "../api/types.ts";
import {
  applyLiveScanPatch,
  liveScanWantsListRefresh,
} from "./scan-display.ts";

type LiveScanListDecision = {
  rows: CodeScan[];
  known: boolean;
  statusChanged: boolean;
  /** Requests one scan-list refresh. */
  shouldList: boolean;
};

/** Refreshes scan history for unknown scans and status changes. */
export function createLiveScanListGate() {
  const unknownTried = new Map<string, CodeScanEvent["status"]>();
  return {
    reset: () => unknownTried.clear(),
    decide(live: CodeScanEvent, history: CodeScan[]): LiveScanListDecision {
      const patched = applyLiveScanPatch(history, live);
      if (!patched.known) {
        const previous = unknownTried.get(live.scan_id);
        unknownTried.delete(live.scan_id);
        unknownTried.set(live.scan_id, live.status);
        if (unknownTried.size > 128) unknownTried.delete(unknownTried.keys().next().value!);
        return { ...patched, shouldList: previous === undefined || liveScanWantsListRefresh(true, previous !== live.status, live.status) };
      }
      unknownTried.delete(live.scan_id);
      return {
        ...patched,
        shouldList: liveScanWantsListRefresh(
          true,
          patched.statusChanged,
          live.status,
        ),
      };
    },
  };
}
