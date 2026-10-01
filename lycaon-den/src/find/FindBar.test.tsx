import { afterEach, describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { onCleanup, onMount } from "solid-js";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { search } from "@codemirror/search";
import { FindBar } from "./FindBar.tsx";
import {
  findController,
  openFind,
  registerFindableView,
  resetFindControllerForTests,
  setFindQuery,
  setPrimaryFindableView,
} from "./find-controller.ts";
import { registerFindRevealHost } from "./find-reveal.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";

function FindBarStubHost(props: { corpus: string }) {
  let corpusEl: HTMLDivElement | undefined;

  onMount(() => {
    const el = corpusEl;
    if (!el) return;
    const unregister = registerFindableView({
      id: "stub-host",
      rootEl: () => el,
      scrollMatchIntoView: (match) => {
        try {
          const node = match.range.startContainer;
          const host =
            node instanceof Element ? node : node.parentElement;
          host?.scrollIntoView({ block: "nearest" });
        } catch {
          /* Scrolling may be unavailable. */
        }
      },
    });
    setPrimaryFindableView("stub-host");
    openFind();
    onCleanup(() => {
      unregister();
      setPrimaryFindableView(null);
    });
  });

  return (
    <div>
      <FindBar />
      <div
        ref={(el) => {
          corpusEl = el;
        }}
        data-testid="find-stub-corpus"
      >
        {props.corpus}
      </div>
    </div>
  );
}

function FindBarCollapsedHost() {
  let rootEl: HTMLDivElement | undefined;
  let detailsEl: HTMLDetailsElement | undefined;
  let collapsed = true;

  onMount(() => {
    const root = rootEl;
    const details = detailsEl;
    if (!root || !details) return;
    const unregisterView = registerFindableView({
      id: "stub-collapsed",
      rootEl: () => root,
      scrollMatchIntoView: () => {},
    });
    const unregisterReveal = registerFindRevealHost({
      id: "reveal-collapsed",
      hostEl: () => details,
      isCollapsed: () => collapsed,
      revealForFind: () => {
        collapsed = false;
        details.open = true;
        return () => {
          collapsed = true;
          details.open = false;
        };
      },
    });
    setPrimaryFindableView("stub-collapsed");
    openFind();
    onCleanup(() => {
      unregisterView();
      unregisterReveal();
      setPrimaryFindableView(null);
    });
  });

  return (
    <div>
      <FindBar />
      <div
        ref={(el) => {
          rootEl = el;
        }}
        data-testid="find-stub-corpus"
      >
        <details
          ref={(el) => {
            detailsEl = el;
          }}
        >
          <p>collapsed-needle here</p>
        </details>
      </div>
    </div>
  );
}

function FindBarEditorHost(props: { doc: string }) {
  let view: EditorView | undefined;

  onMount(() => {
    const parent = document.createElement("div");
    document.body.appendChild(parent);
    view = new EditorView({
      state: EditorState.create({
        doc: props.doc,
        extensions: [search(), EditorState.allowMultipleSelections.of(true)],
      }),
      parent,
    });
    const unregister = registerFindableView({
      id: "files-editor",
      provider: "codemirror",
      rootEl: () => parent,
      scrollMatchIntoView: () => {},
      getEditorView: () => view ?? null,
    });
    setPrimaryFindableView("files-editor");
    openFind();
    onCleanup(() => {
      unregister();
      setPrimaryFindableView(null);
      view?.destroy();
    });
  });

  return <FindBar />;
}

afterEach(() => {
  resetFindControllerForTests();
  document.body.replaceChildren();
});

describe("FindBar", () => {
  it.each([false, true])("keeps find option names and behavior when compact=%s", async (compact) => {
    render(() => <FindBarEditorHost doc="alpha Alpha" />);
    const bar = await screen.findByTestId("find-bar");
    // Compact labels preserve the full accessible names.
    if (compact) {
      for (const label of bar.querySelectorAll<HTMLElement>(".den-find-bar__full-label")) {
        label.style.display = "none";
      }
    }
    const matchCase = screen.getByRole("checkbox", { name: "Match case" }) as HTMLInputElement;
    const scope = screen.getByRole("checkbox", { name: "Find in selection" }) as HTMLInputElement;
    expect(scope.disabled).toBe(true);
    fireEvent.input(screen.getByTestId("find-bar-input"), { target: { value: "alpha" } });
    expect(screen.getByTestId("find-bar-count").textContent).toMatch(/of 2/);
    fireEvent.click(matchCase);
    expect(matchCase.checked).toBe(true);
    expect(screen.getByTestId("find-bar-count").textContent).toMatch(/of 1/);
  });

  it("renders with accessible name and updates match count", async () => {
    render(() => <FindBarStubHost corpus="alpha beta alpha" />);
    const bar = await screen.findByTestId("find-bar");
    expect(bar.getAttribute("aria-label")).toBe("Find in view");
    expect(bar.getAttribute("role")).toBe("search");
    expect(bar.classList.contains("den-editor-command-bar")).toBe(true);

    const input = screen.getByTestId("find-bar-input");
    fireEvent.input(input, { target: { value: "alpha" } });
    expect(screen.getByTestId("find-bar-count").textContent).toMatch(/1 of 2/);
    expect(
      screen.getByTestId("find-stub-corpus").querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`),
    ).toHaveLength(2);

    fireEvent.click(screen.getByTestId("find-bar-next"));
    expect(screen.getByTestId("find-bar-count").textContent).toMatch(/2 of 2/);

    fireEvent.click(screen.getByTestId("find-bar-close"));
    expect(screen.queryByTestId("find-bar")).toBeNull();
  });

  it("shows N in collapsed while hosts stay closed", async () => {
    render(() => <FindBarCollapsedHost />);
    await screen.findByTestId("find-bar");
    setFindQuery("needle");
    expect(findController.collapsedCount()).toBeGreaterThanOrEqual(1);
    expect(screen.getByTestId("find-bar-count").textContent).toMatch(
      /in collapsed/,
    );
  });

  it("Find everywhere carries only the query", async () => {
    render(() => <FindBarStubHost corpus="needle" />);
    await screen.findByTestId("find-bar");
    setFindQuery("needle");

    const button = screen.getByTestId("find-bar-everywhere");
    expect(button.getAttribute("data-tip")).toContain("carries this query");
    expect(button.getAttribute("data-tip")).not.toContain("Match case");
  });

  it("offers no replace disclosure where the view cannot be written", async () => {
    render(() => <FindBarStubHost corpus="alpha" />);
    await screen.findByTestId("find-bar");
    expect(screen.queryByTestId("find-bar-replace-toggle")).toBeNull();
    expect(screen.queryByTestId("find-bar-replacement")).toBeNull();
  });

  it("keeps replace behind a disclosure on a writable buffer", async () => {
    render(() => <FindBarEditorHost doc="alpha beta alpha" />);
    await screen.findByTestId("find-bar");

    const toggle = screen.getByTestId("find-bar-replace-toggle");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTestId("find-bar-replacement")).toBeNull();

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByTestId("find-bar-replacement")).toBeTruthy();
    expect(screen.getByTestId("find-bar-replace-all")).toBeTruthy();

    fireEvent.click(toggle);
    expect(screen.queryByTestId("find-bar-replacement")).toBeNull();
  });

  it("replaces the current match from the replace field", async () => {
    render(() => <FindBarEditorHost doc="alpha beta alpha" />);
    await screen.findByTestId("find-bar");
    fireEvent.click(screen.getByTestId("find-bar-replace-toggle"));
    fireEvent.input(screen.getByTestId("find-bar-input"), {
      target: { value: "alpha" },
    });
    const replacement = screen.getByTestId("find-bar-replacement");
    fireEvent.input(replacement, { target: { value: "omega" } });
    fireEvent.keyDown(replacement, { key: "Enter" });

    expect(document.querySelector(".cm-content")?.textContent).toContain(
      "omega",
    );
  });
});
