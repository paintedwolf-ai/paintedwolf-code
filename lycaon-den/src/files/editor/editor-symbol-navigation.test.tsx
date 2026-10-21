// @vitest-environment jsdom
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { expect, it, vi } from "vitest";
import type { SourceSymbol } from "../../api/types.ts";
import { FilesSymbolMenu, type createEditorSymbolNavigation } from "./editor-symbol-navigation.tsx";

it("leaves IME commit and candidate navigation in the symbol filter before ordinary Enter selects", () => {
  const trigger = document.createElement("button");
  document.body.append(trigger);
  const [symbolFilter, setSymbolFilter] = createSignal("");
  const [symbolActive, setSymbolActive] = createSignal(0);
  const rows: SourceSymbol[] = [{ name: "target", kind: "function", line: 7 }];
  const symbols = {
    symbolMenuOpen: () => true, symbolFilter, setSymbolFilter, symbolActive, setSymbolActive,
    symbolListId: "qualified-symbol-list", symbols: () => rows, symbolsStatus: () => "ready" as const,
    filteredSymbols: () => rows, closeSymbolMenu: vi.fn(), toggleSymbolMenu: vi.fn(),
    selectSymbol: vi.fn(), moveSymbolActive: vi.fn(), symbolTrigger: () => trigger,
    observeSource: vi.fn(), setFilterElement: vi.fn(), setMenuElement: vi.fn(),
  } satisfies ReturnType<typeof createEditorSymbolNavigation>;
  render(() => <FilesSymbolMenu symbols={symbols} />);
  const input = screen.getByRole("combobox", { name: "Filter symbols" });
  fireEvent.input(input, { target: { value: "target" } });
  for (const composition of [{ isComposing: true }, { keyCode: 229 }]) {
    for (const key of ["Enter", "Escape", "ArrowDown"]) fireEvent.keyDown(input, { key, ...composition });
    expect(symbols.selectSymbol).not.toHaveBeenCalled();
    expect(symbols.closeSymbolMenu).not.toHaveBeenCalled();
    expect(symbols.moveSymbolActive).not.toHaveBeenCalled();
  }
  expect(symbolFilter()).toBe("target");
  fireEvent.keyDown(input, { key: "Enter" });
  expect(symbols.selectSymbol).toHaveBeenCalledWith(7);
  trigger.remove();
});
