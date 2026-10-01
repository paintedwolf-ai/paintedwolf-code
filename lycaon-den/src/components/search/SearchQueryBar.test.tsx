import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { SearchQueryBar } from "./SearchQueryBar.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";

describe("SearchQueryBar", () => {
  it("focuses its query when the stage first mounts active", async () => {
    render(() => (
      <ResidentPresenceProvider presence="active">
        <SearchQueryBar
          query=""
          originProjectId="proj-a"
          originName="Alpha"
          onQueryChange={vi.fn()}
        />
      </ResidentPresenceProvider>
    ));

    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByTestId("search-query-input"));
    });
  });

  it("focuses the query whenever its retained stage becomes active", async () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <SearchQueryBar
          query=""
          originProjectId="proj-a"
          originName="Alpha"
          onQueryChange={vi.fn()}
        />
      </ResidentPresenceProvider>
    ));
    const input = screen.getByTestId("search-query-input");
    expect(document.activeElement).not.toBe(input);

    setPresence("active");
    await waitFor(() => expect(document.activeElement).toBe(input));
    setPresence("idle");
    document.body.focus();
    setPresence("active");
    await waitFor(() => expect(document.activeElement).toBe(input));
  });

  it("anchors the first-time search tip to the query field", () => {
    render(() => (
      <SearchQueryBar
        query=""
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));

    expect(
      screen
        .getByTestId("search-query-bar")
        .getAttribute("data-first-time-tip-anchor"),
    ).toBeNull();
    expect(
      document
        .querySelector(".den-search-query__field")
        ?.getAttribute("data-first-time-tip-anchor"),
    ).toBe("project-search");
  });

  it("renders parse lint for invalid DSL", () => {
    // An allowlisted field with no value still lints. A non-allowlisted
    // `word:` is free-text prose (transcript paste), not a query error.
    render(() => (
      <SearchQueryBar
        query="kind:"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));
    expect(screen.getByTestId("search-query-lint").textContent).toContain(
      "Add a value after kind:",
    );
  });

  it("hints at quoting when several bare words are typed", () => {
    render(() => (
      <SearchQueryBar
        query="deliverables are not nameable"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("search-query-lint")).toBeNull();
    expect(screen.getByTestId("search-query-hint").textContent).toContain(
      "quote the text",
    );
  });

  it("treats a non-allowlisted word: prefix as prose, not a lint error", () => {
    render(() => (
      <SearchQueryBar
        query="badfield:x"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("search-query-lint")).toBeNull();
  });

  it("rewrites project scope from the scope control", () => {
    const onQueryChange = vi.fn();
    render(() => (
      <SearchQueryBar
        query="auth"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={onQueryChange}
      />
    ));
    fireEvent.click(screen.getByTestId("search-scope-current"));
    expect(onQueryChange).toHaveBeenCalledWith("auth kind:code project:current");
  });

  it("switches scope to Everything from This project", () => {
    const onQueryChange = vi.fn();
    render(() => (
      <SearchQueryBar
        query="auth project:current"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={onQueryChange}
      />
    ));
    fireEvent.click(screen.getByTestId("search-scope-everything"));
    expect(onQueryChange).toHaveBeenCalledWith("auth");
  });

  it("hides project:current from the editable input when scoped", () => {
    render(() => (
      <SearchQueryBar
        query="project:current"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));
    expect((screen.getByTestId("search-query-input") as HTMLInputElement).value).toBe("");
  });

  it("renders match toggles with pressed state", () => {
    const onMatchChange = vi.fn();
    render(() => (
      <SearchQueryBar
        query="auth"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
        match={{ caseSensitive: true, wholeWord: false, regex: false }}
        onMatchChange={onMatchChange}
      />
    ));
    expect(screen.getByTestId("search-toggle-case").getAttribute("aria-pressed")).toBe(
      "true",
    );
    fireEvent.click(screen.getByTestId("search-toggle-word"));
    expect(onMatchChange).toHaveBeenCalledWith({ wholeWord: true });
  });

  it("shows replacement field and $1 hint when regex replace is on", () => {
    render(() => (
      <SearchQueryBar
        query="auth"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
        match={{ caseSensitive: false, wholeWord: false, regex: true }}
        onMatchChange={vi.fn()}
        replaceMode
        onReplaceModeChange={vi.fn()}
        replacement=""
        onReplacementChange={vi.fn()}
      />
    ));
    const input = screen.getByTestId("search-replace-input") as HTMLInputElement;
    expect(input.placeholder).toContain("$1");
  });

  it("does not duplicate full-stage type selectors inside the query bar", () => {
    const onQueryChange = vi.fn();
    render(() => (
      <SearchQueryBar
        query="project:current"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={onQueryChange}
      />
    ));
    expect(screen.queryByTestId("search-type-filters")).toBeNull();
    expect(screen.getByTestId("search-query-input")).toBeTruthy();
  });

  it("does not render a second active-filter pill band under the field", () => {
    render(() => (
      <SearchQueryBar
        query="kind:code project:current"
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("search-active-filters")).toBeNull();
  });

  it("preserves spaces and colons while typing a query", () => {
    const [query, setQuery] = createSignal("");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId={null}
        originName={null}
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    fireEvent.focus(input);
    // Each input event carries the field's complete value.
    const target = "auth handle:foo";
    for (let i = 1; i <= target.length; i++) {
      fireEvent.input(input, { target: { value: target.slice(0, i) } });
    }
    expect(input.value).toBe("auth handle:foo");
    expect(query()).toBe("auth handle:foo");
  });

  it("keeps the highlighted query aligned while a long input scrolls", () => {
    const [query, setQuery] = createSignal("kind:code a deliberately long query");
    const { container } = render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId={null}
        originName={null}
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    const highlight = container.querySelector(
      ".den-search-query__chips",
    ) as HTMLDivElement;
    input.scrollLeft = 96;
    fireEvent.scroll(input);
    expect(highlight.scrollLeft).toBe(96);

    const longer = `${input.value} with more terms than the visible field can show`;
    fireEvent.input(input, { target: { value: longer } });
    expect(input.value).toBe(longer);
    expect(query()).toBe(longer);
  });

  it("keeps typed text when scoped, without leaking project:current into the input", () => {
    const [query, setQuery] = createSignal("project:current");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    fireEvent.focus(input);
    const target = "kind:web auth";
    for (let i = 1; i <= target.length; i++) {
      fireEvent.input(input, { target: { value: target.slice(0, i) } });
    }
    expect(input.value).toBe("kind:web auth");
    expect(query()).toBe("kind:web auth project:current");
  });

  it("separates typing from an automatic filter chip", () => {
    const [query, setQuery] = createSignal("kind:code project:current");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    input.setSelectionRange(input.value.length, input.value.length);
    input.dispatchEvent(
      new InputEvent("beforeinput", {
        bubbles: true,
        cancelable: true,
        inputType: "insertText",
        data: "a",
      }),
    );

    expect(input.value).toBe("kind:code a");
    expect(query()).toBe("kind:code a project:current");
  });

  it("still allows deliberate edits inside a filter chip", () => {
    const [query, setQuery] = createSignal("kind:code");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId={null}
        originName={null}
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    input.setSelectionRange(6, 6);
    fireEvent.input(input, { target: { value: "kind:xcode" } });

    expect(input.value).toBe("kind:xcode");
    expect(query()).toBe("kind:xcode");
  });

  it("does not show suggestions on an empty focused query", () => {
    render(() => (
      <SearchQueryBar
        query="project:current"
        originProjectId="proj-a"
        originName="Alpha"
        recentQueries={["kind:code", "auth bug"]}
        onQueryChange={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("search-field-suggestions")).toBeNull();
  });

  it("dismisses suggestions with Escape and the close control", async () => {
    const [query, setQuery] = createSignal("project:current");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId="proj-a"
        originName="Alpha"
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    fireEvent.focus(input);
    fireEvent.input(input, { target: { value: "ki" } });
    expect(
      (await screen.findByTestId("search-field-suggestions-scroll")).hasAttribute(
        "data-den-scrollport",
      ),
    ).toBe(true);
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByTestId("search-field-suggestions")).toBeNull();
    fireEvent.input(input, { target: { value: "ki" } });
    expect(await screen.findByTestId("search-field-suggestions")).toBeTruthy();
    fireEvent.click(screen.getByTestId("search-suggestions-close"));
    expect(screen.queryByTestId("search-field-suggestions")).toBeNull();
  });

  it("exposes the active suggestion through combobox semantics", async () => {
    const [query, setQuery] = createSignal("");
    render(() => (
      <SearchQueryBar
        query={query()}
        originProjectId={null}
        originName={null}
        facets={[
          {
            key: "kind",
            values: [
              { value: "code", count: 4 },
              { value: "message", count: 3 },
            ],
          },
        ]}
        onQueryChange={setQuery}
      />
    ));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    fireEvent.focus(input);
    fireEvent.input(input, { target: { value: "kind:" } });
    const list = await screen.findByRole("listbox");
    const options = screen.getAllByRole("option");

    expect(input.getAttribute("role")).toBe("combobox");
    expect(input.getAttribute("aria-controls")).toBe(list.id);
    expect(input.getAttribute("aria-activedescendant")).toBe(options[0]!.id);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input.getAttribute("aria-activedescendant")).toBe(options[1]!.id);
    expect(options[1]!.getAttribute("aria-selected")).toBe("true");
  });
});
