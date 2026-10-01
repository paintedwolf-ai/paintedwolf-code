import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { Lexer, type Token } from "marked";
import { MarkdownBody } from "./MarkdownBody.tsx";
import {
  registerOpenSourceProjectLookup,
  registerOpenSourceSink,
  resetOpenSourceForTests,
} from "../../platform/navigation/open-source.ts";
import { resetExternalOpenPrefsForTests } from "../../settings/editor/external-open-prefs.ts";

const roots = {
  roots: [{ id: "root-1", path: "/Users/me/repo", is_primary: true }],
};

describe("MarkdownBody", () => {
  beforeEach(() => {
    resetOpenSourceForTests();
    resetExternalOpenPrefsForTests();
  });

  it("renders ATX headings as h2", () => {
    render(() => <MarkdownBody source={"Intro\n\n## Section title\n\nBody"} />);
    const h2 = screen.getByRole("heading", { level: 2 });
    expect(h2.textContent).toBe("Section title");
  });

  it("updates between raw Markdown and resolved token blocks", () => {
    const [source, setSource] = createSignal<string | Token[]>("## Raw source");
    const { container } = render(() => <MarkdownBody source={source()} />);
    expect(screen.getByRole("heading").textContent).toBe("Raw source");

    const tokens = Lexer.lex("[Reference][ref]\n\n[ref]: https://example.com/first");
    setSource(tokens);
    expect(screen.queryByRole("heading")).toBeNull();
    expect(screen.getByRole("link").getAttribute("href")).toBe("https://example.com/first");

    setSource(Lexer.lex("[Reference][ref]\n\n[ref]: https://example.com/second"));
    expect(screen.getByRole("link").getAttribute("href")).toBe("https://example.com/second");

    setSource("");
    expect(container.querySelector(".markdown-body")?.innerHTML).toBe("");
  });

  it("renders heading after prose when glued (preprocessed)", () => {
    render(() => <MarkdownBody source={"Intro line\n## Glued heading"} />);
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "Glued heading",
    );
  });

  it("renders ATX heading missing space after hashes", () => {
    render(() => <MarkdownBody source={"##NoSpaceTitle\n\nParagraph"} />);
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "NoSpaceTitle",
    );
  });

  it("renders file-path links as source navigation buttons when projectId is given", () => {
    render(() => (
      <MarkdownBody source={"See [a](src/foo.ts)"} projectId="p1" />
    ));
    const button = screen.getByRole("button", { name: "a" });
    expect(button.getAttribute("data-project-id")).toBe("p1");
    expect(button.getAttribute("data-den-source-path")).toBe("src/foo.ts");
  });

  it("opens file-path links via openSourceLocation on click", () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    render(() => (
      <MarkdownBody source={"See [a](src/foo.ts:12)"} projectId="p1" />
    ));

    fireEvent.click(screen.getByRole("button", { name: "a" }));

    expect(sink).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "p1",
      path: "src/foo.ts",
      absolutePath: "/Users/me/repo/src/foo.ts",
      rootId: "root-1",
      line: 12,
    });
  });

  it("does not open a file-path link when the click ends selecting its text", () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    render(() => (
      <MarkdownBody source={"See [label](src/foo.ts:12)"} projectId="p1" />
    ));

    const link = screen.getByRole("button", { name: "label" });
    document.getSelection()!.selectAllChildren(link);
    try {
      fireEvent.click(link, { detail: 1 });
      expect(sink).not.toHaveBeenCalled();
    } finally {
      document.getSelection()!.removeAllRanges();
    }
  });

  it("opens a file-path link while other text stays selected", () => {
    const sink = vi.fn();
    registerOpenSourceSink(sink);
    registerOpenSourceProjectLookup(() => roots);

    render(() => (
      <>
        <p data-testid="other">Earlier message</p>
        <MarkdownBody source={"See [label](src/foo.ts:12)"} projectId="p1" />
      </>
    ));

    document.getSelection()!.selectAllChildren(screen.getByTestId("other"));
    try {
      fireEvent.click(screen.getByRole("button", { name: "label" }), { detail: 1 });
      expect(sink).toHaveBeenCalledTimes(1);
    } finally {
      document.getSelection()!.removeAllRanges();
    }
  });

  it("offers Add to chat on source-path context menu when rootRefs resolve", async () => {
    render(() => (
      <MarkdownBody
        source={"See [a](src/foo.ts)"}
        projectId="p1"
        rootRefs={roots.roots}
      />
    ));

    fireEvent.contextMenu(screen.getByRole("button", { name: "a" }));

    const add = await screen.findByTestId("menu-add-to-chat");
    expect(add.textContent).toBe("Add to chat");
  });

  it("renders file-path links as plain text when projectId is absent", () => {
    render(() => <MarkdownBody source={"See [a](src/foo.ts)"} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("a")).toBeTruthy();
  });
});
