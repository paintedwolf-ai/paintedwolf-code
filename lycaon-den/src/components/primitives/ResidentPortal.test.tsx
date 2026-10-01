import { render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it } from "vitest";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import { ResidentPortal } from "./ResidentPortal.tsx";

describe("resident portals", () => {
  it("inherits an ancestor's interaction hold without changing the nested surface identity", () => {
    const [interactive, setInteractive] = createSignal(true);
    render(() => <ResidentPresenceProvider presence="active" interactive={interactive()}>
      <ResidentPresenceProvider presence="active">
        <ResidentPortal><input aria-label="Nested draft" /></ResidentPortal>
      </ResidentPresenceProvider>
    </ResidentPresenceProvider>);
    const input = screen.getByRole("textbox");
    setInteractive(false);
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(input.closest("[data-resident-portal]")?.getAttribute("data-resident-portal")).toBe("active");
    setInteractive(true);
    expect(screen.getByRole("textbox")).toBe(input);
  });

  it("retains a dialog's fields while hiding it with its surface", () => {
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => <ResidentPresenceProvider presence={presence()}>
      <ResidentPortal><input aria-label="Draft" /></ResidentPortal>
    </ResidentPresenceProvider>);
    const input = screen.getByRole("textbox") as HTMLInputElement;
    input.value = "unsaved";
    setPresence("idle");
    expect(screen.queryByRole("textbox")).toBeNull();
    expect((input.closest("[data-resident-portal]") as HTMLElement).inert).toBe(true);
    setPresence("active");
    expect(screen.getByRole("textbox")).toBe(input);
    expect(input.value).toBe("unsaved");
  });
});
