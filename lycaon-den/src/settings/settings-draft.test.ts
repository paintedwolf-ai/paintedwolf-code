import { describe, expect, it } from "vitest";
import { createSettingsDraft } from "./settings-draft.ts";

describe("settings drafts", () => {
  it("hydrates clean fields and preserves typing through a refresh and an older save reply", () => {
    const draft = createSettingsDraft({ command: "" });
    const sync = (apply: () => void) => apply();
    draft.receive({ command: "first" }, sync);
    expect(draft.value().command).toBe("first");
    draft.set({ command: "second" });
    draft.receive({ command: "first" }, sync);
    expect(draft.value().command).toBe("second");
    draft.set({ command: "third" });
    draft.receive({ command: "second" }, sync);
    expect(draft.value().command).toBe("third");
    expect(draft.dirty()).toBe(true);
    draft.receive({ command: "third" }, sync);
    expect(draft.dirty()).toBe(false);
    draft.receive({ command: "external" }, sync);
    expect(draft.value().command).toBe("external");
  });
});
