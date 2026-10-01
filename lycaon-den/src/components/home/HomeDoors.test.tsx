import { describe, expect, it, vi } from "vitest";
import { render, fireEvent } from "@solidjs/testing-library";
import { HomeView } from "./HomeView.tsx";
import { loaded } from "../../store/load-state.ts";

function renderHome(over: { onOpenFolder?: () => void; onCloneRepo?: () => void } = {}) {
  return render(() => (
    <HomeView
      summaries={[]}
      section="recents"
      registry={loaded(null)}
      onSubmitIdea={vi.fn(() => true)}
      onOpenFolder={over.onOpenFolder ?? vi.fn()}
      onCloneRepo={over.onCloneRepo ?? vi.fn()}
      onOpenProject={vi.fn()}
      onToggleStar={vi.fn()}
      onRename={vi.fn()}
      onAttachFolder={vi.fn()}
      onPromote={vi.fn()}
      onDelete={vi.fn()}
    />
  ));
}

describe("HomeView existing-code doors", () => {
  it("fires onOpenFolder and onCloneRepo", () => {
    const onOpenFolder = vi.fn();
    const onCloneRepo = vi.fn();
    const { getByTestId } = renderHome({ onOpenFolder, onCloneRepo });
    fireEvent.click(getByTestId("home-open-folder"));
    fireEvent.click(getByTestId("home-clone-repo"));
    expect(onOpenFolder).toHaveBeenCalledTimes(1);
    expect(onCloneRepo).toHaveBeenCalledTimes(1);
  });
});
