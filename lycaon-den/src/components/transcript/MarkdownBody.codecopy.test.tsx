import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MarkdownBody } from "./MarkdownBody.tsx";

const writeText = vi.fn(async (_text: string) => undefined);
Object.defineProperty(navigator, "clipboard", {
  value: { writeText },
  configurable: true,
});

afterEach(() => {
  cleanup();
  writeText.mockClear();
});

describe("code block copy", () => {
  it("adds one copy control per fenced block", async () => {
    const { container } = render(() => (
      <MarkdownBody source={"text\n\n```go\nfmt.Println(1)\n```\n\n```sh\nls -l\n```\n"} />
    ));
    await waitFor(() =>
      expect(container.querySelectorAll("[data-testid=code-block-copy]").length).toBe(2),
    );
  });

  it("copies the block source, not the highlighted markup", async () => {
    const { container } = render(() => (
      <MarkdownBody source={"```go\nfmt.Println(1)\n```\n"} />
    ));
    await waitFor(() =>
      expect(container.querySelector("[data-testid=code-block-copy]")).toBeTruthy(),
    );
    const button = container.querySelector(
      "[data-testid=code-block-copy]",
    ) as HTMLButtonElement;
    button.click();
    expect(writeText).toHaveBeenCalledTimes(1);
    const copied = writeText.mock.calls[0]?.[0] ?? "";
    expect(copied).toContain("fmt.Println(1)");
    expect(copied).not.toContain("<span");
  });

  it("adds nothing to prose with no code blocks", async () => {
    const { container } = render(() => <MarkdownBody source={"just words\n"} />);
    await waitFor(() => expect(container.textContent).toContain("just words"));
    expect(container.querySelectorAll("[data-testid=code-block-copy]").length).toBe(0);
  });

  it("does not stack duplicate buttons when the source updates", async () => {
    const { container } = render(() => (
      <MarkdownBody source={"```go\nfmt.Println(1)\n```\n"} />
    ));
    await waitFor(() =>
      expect(container.querySelectorAll("[data-testid=code-block-copy]").length).toBe(1),
    );
    await waitFor(() =>
      expect(container.querySelectorAll("[data-testid=code-block-copy]").length).toBe(1),
    );
  });
});
