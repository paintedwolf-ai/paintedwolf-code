import { fileEditPreviewFixture } from "../test/file-edit-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { render, screen, waitFor, cleanup } from "@solidjs/testing-library";
import { stubFilesClient } from "../test/source-client-fixture.ts";
import { FileEditDiff } from "./FileEditDiff.tsx";
import {
  foldFileEdits,
  singleFileEditFold,
} from "../chat/file-edit/file-edit-fold.ts";
import type { FileEditStep } from "../chat/file-edit/file-edit-fold.ts";
import { resetFindControllerForTests } from "../find/find-controller.ts";
import { transcriptDisclosureKey } from "../chat/transcript/presentation/transcript-disclosure-key.ts";
import {
  listFindRevealHosts,
  resetFindRevealHostsForTests,
} from "../find/find-reveal.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../chat/stream/transcript-viewport.tsx";
import {
  RowAccordionProvider,
  createRowAccordion,
} from "../chat/transcript/presentation/row-accordion.ts";

const client = stubFilesClient();
vi.mock("../platform/connection/app-connection.ts", async original => ({
  ...await original<typeof import("../platform/connection/app-connection.ts")>(), getLycaonClient: () => client,
}));

afterEach(() => {
  cleanup();
  resetFindControllerForTests();
  resetFindRevealHostsForTests();
  document.body.replaceChildren();
});

const V1 = "one\ntwo\nthree\n";
const V2 = "one\nTWO\nthree\n";
const V3 = "one\nTWO\nthree\nfour\n";

function step(
  key: string,
  before: string | null,
  after: string,
  tool = "edit",
): FileEditStep {
  return { key, snapshot: fileEditPreviewFixture({ path: "pipeline.py", before, after }), tool };
}

function multiWriteFold() {
  return foldFileEdits([
    step("t1", V1, V2),
    step("t2", V2, V3),
    step("t3", V3, V2),
  ])[0]!;
}

describe("FileEditDiff", () => {
  it.each([true, false])("preserves following=%s when a diff opens and closes", (following) => {
    const controller = createTranscriptViewportController({ sessionId: () => "s1" });
    if (!following) controller.stopFollowing();
    render(() => (
      <TranscriptViewportProvider value={controller}>
        <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />
      </TranscriptViewportProvider>
    ));

    screen.getByLabelText("Show diff for pipeline.py").click();
    expect(controller.following()).toBe(following);
    screen.getByLabelText("Collapse diff for pipeline.py").click();
    expect(controller.following()).toBe(following);
  });
  it("shows the range's stat and the write count for a path written more than once", async () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    // Three writes; the range is one changed line.
    await waitFor(() => expect(screen.getByTestId("file-edit-diff-stat").textContent).toBe("+1−1"));
    expect(screen.getByTestId("file-edit-diff-writes").textContent).toBe(
      "3 edits",
    );
  });

  it("keeps the comparison it asked for when the same writes arrive as a rebuilt fold", async () => {
    const opens = vi.spyOn(client, "createSourceView");
    const reads = vi.spyOn(client, "getSourceView");
    try {
      const [fold, setFold] = createSignal(multiWriteFold());
      render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={fold()} />);
      await waitFor(() => expect(screen.getByTestId("file-edit-diff-stat").textContent).toBe("+1−1"));
      const requested = opens.mock.calls.length + reads.mock.calls.length;
      setFold(multiWriteFold());
      await new Promise((resolve) => setTimeout(resolve, 20));
      expect(screen.getByTestId("file-edit-diff-writes").textContent).toBe("3 edits");
      expect(opens.mock.calls.length + reads.mock.calls.length).toBe(requested);
    } finally {
      opens.mockRestore();
      reads.mockRestore();
    }
  });

  it("closes the header with its disclosure, never opens with it", () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    const header = screen
      .getByTestId("file-edit-diff")
      .querySelector(".den-file-edit-diff-header")!;
    expect(header.lastElementChild).toBe(
      header.querySelector(".den-file-edit-diff-disclosure"),
    );
  });

  it("rotates the disclosure caret with the diff", () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    const caret = document.querySelector(".den-file-edit-diff-caret")!;
    expect(caret.classList.contains("den-file-edit-diff-caret--open")).toBe(
      false,
    );

    screen.getByLabelText("Show diff for pipeline.py").click();
    expect(caret.classList.contains("den-file-edit-diff-caret--open")).toBe(
      true,
    );
  });

  it("gives the header's empty space to the diff, not to the path", () => {
    render(() => (
      <FileEditDiff projectId="p1" sessionId="s1"
        fold={multiWriteFold()}

      />
    ));
    const header = screen
      .getByTestId("file-edit-diff")
      .querySelector(".den-file-edit-diff-header")!;
    const fill = header.querySelector(".den-file-edit-diff-fill")!;
    const link = header.querySelector('[data-testid="source-path-link"]')!;
    expect(fill).toBeTruthy();
    expect(
      link.compareDocumentPosition(fill) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("names no writes and offers no strip for a single write", () => {
    render(() => (
      <FileEditDiff projectId="p1" sessionId="s1"
        fold={singleFileEditFold("t1", fileEditPreviewFixture({ path: "a.ts", before: V1, after: V2 }))}

      />
    ));
    expect(screen.queryByTestId("file-edit-diff-writes")).toBeNull();
    expect(screen.queryByTestId("file-edit-diff-strip")).toBeNull();
  });

  it("shows the revision strip only while the diff is open", async () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    const card = screen.getByTestId("file-edit-diff");
    expect(screen.queryByTestId("file-edit-diff-strip")).toBeNull();

    screen.getByLabelText("Show diff for pipeline.py").click();
    expect(card.getAttribute("data-expanded")).toBe("true");
    expect(screen.getByTestId("file-edit-diff-strip")).toBeTruthy();

    const secondEdit = screen.getByLabelText("Edit 2 of 3, edit");
    secondEdit.click();
    expect(secondEdit.getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByText("Edit 2 of 3")).toBeNull();
    // Write 2 added a line; the range did not.
    expect(screen.getByTestId("file-edit-diff-stat").textContent).toBe("+1−0");

    const net = screen.getByText("Net");
    net.click();
    expect(net.getAttribute("aria-pressed")).toBe("true");
    await waitFor(() => expect(screen.getByTestId("file-edit-diff-stat").textContent).toBe("+1−1"));
  });

  it("hides the strip with the diff it belongs to", () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    screen.getByLabelText("Show diff for pipeline.py").click();
    expect(screen.getByTestId("file-edit-diff-strip")).toBeTruthy();

    screen.getByLabelText("Collapse diff for pipeline.py").click();
    expect(
      screen.getByTestId("file-edit-diff").getAttribute("data-expanded"),
    ).toBe("false");
    expect(screen.queryByTestId("file-edit-diff-strip")).toBeNull();
    expect(screen.getByTestId("file-edit-diff-writes").textContent).toBe(
      "3 edits",
    );
  });

  it("keeps a card whose writes cancelled out, and says so", () => {
    const fold = foldFileEdits([step("t1", V1, V2), step("t2", V2, V1)])[0]!;
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={fold} />);
    expect(screen.getByTestId("file-edit-diff-stat").textContent).toBe(
      "no net change",
    );
    expect(screen.getByTestId("file-edit-diff-writes").textContent).toBe(
      "2 edits",
    );
  });

  it("registers find under the fold, so two cards on one path do not share an id", () => {
    render(() => (
      <>
        <FileEditDiff projectId="p1" sessionId="s1"
          fold={foldFileEdits([step("t1", V1, V2)])[0]!}
          disclosureKey={transcriptDisclosureKey.diffFile("t1")}
        />
        <FileEditDiff projectId="p1" sessionId="s1"
          fold={foldFileEdits([step("t9", V2, V3)])[0]!}
          disclosureKey={transcriptDisclosureKey.diffFile("t9")}
        />
      </>
    ));
    const ids = listFindRevealHosts().map((host) => host.id);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(2);
    expect(listFindRevealHosts().map(host => host.hostEl()?.closest("[data-disclosure-key]")?.getAttribute("data-disclosure-key")).sort()).toEqual([transcriptDisclosureKey.diffFile("t1"), transcriptDisclosureKey.diffFile("t9")]);
  });

  it("shows a new file as file details, a deleted file as removed details, and an edit as a diff", async () => {
    render(() => (
      <>
        <FileEditDiff projectId="p1" sessionId="s1"
          fold={singleFileEditFold("t1", fileEditPreviewFixture({ path: "new.py", before: null, after: V1 }))}
        />
        <FileEditDiff projectId="p1" sessionId="s1"
          fold={singleFileEditFold("t2", fileEditPreviewFixture({ path: "old.py", before: V1, after: V2 }))}
        />
        <FileEditDiff projectId="p1" sessionId="s1"
          fold={singleFileEditFold("t3", fileEditPreviewFixture({ path: "gone.py", before: V1, after: "", deleted: true }))}
        />
      </>
    ));
    for (const path of ["new.py", "old.py", "gone.py"]) screen.getByLabelText(`Show diff for ${path}`).click();
    const card = (path: string) => document.querySelector<HTMLElement>(`.den-file-edit-diff[data-path="${path}"]`)!;
    // Loaded rows carry their source row; a range placeholder does not.
    const rows = (path: string) => [...card(path).querySelectorAll<HTMLElement>(".cm-den-reader .cm-line[data-source-row]")];
    await waitFor(() => {
      expect(card("new.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeTruthy();
      expect(rows("old.py")).toHaveLength(4);
      expect(card("gone.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeTruthy();
    });
    expect(rows("new.py")).toHaveLength(0);
    expect(rows("gone.py")).toHaveLength(0);
    expect(card("new.py").querySelector('[data-testid="source-change-chip"]')?.textContent).toBe("New file");
    expect(card("new.py").querySelector(".den-file-edit-diff-details-badge")?.textContent).toBe("New file");
    expect(card("new.py").querySelector(".den-file-edit-diff-details-lines")?.textContent).toBe("3 lines added");
    expect(card("new.py").querySelector(".den-file-edit-diff-details-btn")?.textContent).toBe("View file");

    const classes = (path: string) => rows(path).map(line => line.className).join(" | ");
    expect(classes("old.py")).toMatch(/cm-den-reader-(add|delete)/);
    expect(card("old.py").querySelector('[data-testid="source-change-chip"]')).toBeNull();
    expect(card("old.py").querySelector('[data-testid="file-edit-diff-details"]')).toBeNull();

    expect(card("gone.py").querySelector('[data-testid="source-change-chip"]')?.textContent).toBe("Deleted file");
    expect(card("gone.py").querySelector(".den-source-path-plain")?.getAttribute("data-file-change")).toBe("deleted");
    expect(card("gone.py").querySelector(".den-file-edit-diff-details-badge")?.textContent).toBe("Deleted file");
    expect(card("gone.py").querySelector(".den-file-edit-diff-details-lines")?.textContent).toBe("3 lines removed");
    expect(card("gone.py").querySelector(".den-file-edit-diff-details-btn")?.textContent).toBe("View removed contents");
  });

  it("reverses an in-flight close instead of ignoring the click", () => {
    render(() => <FileEditDiff projectId="p1" sessionId="s1" fold={multiWriteFold()} />);
    const card = screen.getByTestId("file-edit-diff");
    const animations: Animation[] = [];
    card.animate = vi.fn(() => {
      const anim = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
      animations.push(anim);
      return anim;
    });
    Object.defineProperty(card, "scrollHeight", { value: 200, configurable: true });

    const showButton = screen.getByLabelText("Show diff for pipeline.py");
    showButton.click();
    expect(card.getAttribute("data-expanded")).toBe("true");

    const collapseButton = screen.getByLabelText("Collapse diff for pipeline.py");
    collapseButton.click();

    expect(animations).toHaveLength(2);

    // Reverses in-flight close.
    collapseButton.click();

    expect(animations).toHaveLength(3);
    expect(animations[1]!.cancel).toHaveBeenCalled();
  });

  it("opens on mount when row accordion held the row open even when chrome is row", () => {
    const accordion = createRowAccordion();
    const fold = multiWriteFold();
    accordion.recordOpen?.(fold.key, true);
    render(() => (
      <RowAccordionProvider value={accordion}>
        <FileEditDiff
          projectId="p1"
          sessionId="s1"
          fold={fold}
          chrome="row"
        />
      </RowAccordionProvider>
    ));
    expect(screen.getByTestId("file-edit-diff").getAttribute("data-expanded")).toBe("true");
  });
});
