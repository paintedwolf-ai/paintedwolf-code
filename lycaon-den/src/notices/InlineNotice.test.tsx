import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { APP_SCOPE } from "./notice-scope.ts";
import { noticeFromWire } from "./notice-model.ts";
import { InlineNotice } from "./InlineNotice.tsx";

describe("InlineNotice", () => {
  it("renders title, message, and suggested action", () => {
    const notice = noticeFromWire(
      {
        code: "remote_requires_https",
        title: "Remote MCP must use HTTPS",
        message: "A remote MCP provider can only be reached over HTTPS.",
        suggested_action: "Use an https:// URL.",
      },
      APP_SCOPE,
    );
    const { getByTestId } = render(() => (
      <InlineNotice notice={notice} testId="mcp-editor-error" />
    ));
    const el = getByTestId("mcp-editor-error");
    expect(el.getAttribute("data-code")).toBe("remote_requires_https");
    expect(el.textContent).toContain("Remote MCP must use HTTPS");
    expect(el.textContent).toContain("HTTPS");
    expect(el.textContent).toContain("https://");
  });

  it("omits a title that repeats the message", () => {
    const notice = noticeFromWire(
      {
        title: "Could not save",
        message: "Could not save",
      },
      APP_SCOPE,
    );
    const { getByTestId } = render(() => (
      <InlineNotice notice={notice} testId="repeat-title" />
    ));
    const el = getByTestId("repeat-title");
    expect(el.querySelector(".den-inline-notice__title")).toBeNull();
    expect(el.querySelector(".den-inline-notice__message")?.textContent).toBe(
      "Could not save",
    );
  });

  it("renders an action button and invokes its callback on click", () => {
    const notice = noticeFromWire(
      {
        code: "extension_state_changed",
        title: "Extension state changed",
        message: "Extensions changed since this view loaded.",
        suggested_action: "Reload the Extensions view and retry the action.",
      },
      APP_SCOPE,
    );
    const onClick = vi.fn();
    const { getByTestId } = render(() => (
      <InlineNotice
        notice={notice}
        testId="extensions-action-error"
        action={{ label: "Reload extensions", onClick, testId: "extensions-action-error-reload" }}
      />
    ));
    const button = getByTestId("extensions-action-error-reload");
    expect(button.textContent).toBe("Reload extensions");
    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("omits the action button when none is given", () => {
    const notice = noticeFromWire(
      { title: "Could not save", message: "Try again." },
      APP_SCOPE,
    );
    const { queryByTestId } = render(() => (
      <InlineNotice notice={notice} testId="no-action" />
    ));
    expect(queryByTestId("inline-notice-action")).toBeNull();
  });

  it("renders nothing when notice is missing", () => {
    const { queryByTestId } = render(() => <InlineNotice notice={undefined} />);
    expect(queryByTestId("inline-notice")).toBeNull();
  });
});
