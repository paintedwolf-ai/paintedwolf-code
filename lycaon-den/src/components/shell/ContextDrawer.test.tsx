import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { ContextDrawer, ContextDrawerHost } from "./ContextDrawer.tsx";

describe("ContextDrawer", () => {
  it("mounts in its chat stage so it covers the stage header", async () => {
    render(() => (
      <ContextDrawerHost>
        <div class="den-shell-stage--chat den-shell-stage">
          <header>Chat tabs</header>
          <main>
            <ContextDrawer
              open
              testId="context-drawer"
              titleId="context-drawer-title"
              title="Context"
              closeLabel="Close context"
              onClose={() => undefined}
            >
              <p>Drawer body</p>
            </ContextDrawer>
          </main>
        </div>
      </ContextDrawerHost>
    ));

    const stage = screen.getByText("Chat tabs").parentElement;
    await waitFor(() =>
      expect(
        screen
          .getByTestId("context-drawer")
          .closest(".den-shell-stage--chat"),
      ).toBe(stage),
    );
    expect(
      screen
        .getByRole("heading", { name: "Context" })
        .closest("header")
        ?.hasAttribute("data-den-chrome"),
    ).toBe(true);
  });

  it("does not render outside a chat stage", () => {
    render(() => (
      <ContextDrawer
        open
        testId="context-drawer"
        titleId="context-drawer-title"
        title="Context"
        closeLabel="Close context"
        onClose={() => undefined}
      >
        <p>Drawer body</p>
      </ContextDrawer>
    ));

    expect(screen.queryByTestId("context-drawer")).toBeNull();
  });
});
