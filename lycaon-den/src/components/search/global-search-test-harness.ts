import {
  afterEach,
  beforeEach,
  vi,
} from "vitest";
import { cleanup } from "@solidjs/testing-library";

import { resetDispatcherForTests } from "../../shortcuts/dispatcher.ts";
import { setAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { resetLayoutStoreForTests } from "../../shell/layout-store.ts";
import { resetSearchMatchPrefsForTests } from "../../search/search-match-prefs.ts";
import { EMPTY_APP_STATE_V1 } from "../../../shared/app-state-types.ts";
export const searchMock = vi.fn();

export const exportMock = vi.fn();

export const replacePreviewMock = vi.fn();

export const replaceApplyMock = vi.fn();

export const reportSearchErrorMock = vi.fn();

vi.mock("../../platform/connection/app-connection.ts", () => ({
  getLycaonClient: () => ({
    search: searchMock,
    exportSearchResults: exportMock,
    previewSearchReplacement: replacePreviewMock,
    applySearchReplacement: replaceApplyMock,
  }),
  noticeReporterFor: () => ({ reportError: reportSearchErrorMock }),
}));

vi.mock("../../store/app-state-snapshot.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../store/app-state-snapshot.ts")>();
  return {
    ...actual,
    loadSharedAppState: vi.fn(async () => actual.getAppStateSnapshot()),
    persistAppState: vi.fn(async (patch) => {
      actual.setAppStateSnapshot({
        ...actual.getAppStateSnapshot(),
        ...patch,
      });
    }),
  };
});

export function setupGlobalSearchViewTests() {
  beforeEach(() => {
    // The test DOM omits scrollIntoView.
    Element.prototype.scrollIntoView = vi.fn();
    localStorage.clear();
    sessionStorage.clear();
    resetSearchMatchPrefsForTests();
    setAppStateSnapshot({ ...EMPTY_APP_STATE_V1 });
    resetDispatcherForTests();
    resetLayoutStoreForTests();
    searchMock.mockReset();
    exportMock.mockReset();
    replacePreviewMock.mockReset();
    replaceApplyMock.mockReset();
    reportSearchErrorMock.mockReset();
    replacePreviewMock.mockResolvedValue({ files: [], state: "ready", issues: [], truncated: false });
    replaceApplyMock.mockResolvedValue({ files: [] });
    searchMock.mockResolvedValue({
      hits: [
        {
          hit_id: "web-b",
          hit_kind: "web",
          source: "tool",
          project_id: "proj-b",
          project_name: "Beta",
          session_id: "sess-b",
          title: "Beta result",
        },
        {
          hit_id: "web-a",
          hit_kind: "web",
          source: "tool",
          project_id: "proj-a",
          project_name: "Alpha",
          session_id: "sess-a",
          title: "Alpha result",
        },
      ],
      status: "complete",
      exhaustive: true,
      total_hits: 2,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "global", filters: [] },
    });
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    resetDispatcherForTests();
  });
}
