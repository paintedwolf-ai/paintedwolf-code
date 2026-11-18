import { expect, it } from "vitest";
import { noteToolCallShownInFlight, wasToolCallShownInFlight } from "./program-tool-visibility.ts";

it("evicts the least recently used call within its session", () => {
  const session = "call-capacity";
  for (let index = 0; index < 2048; index++) noteToolCallShownInFlight(session, String(index));
  expect(wasToolCallShownInFlight(session, "0")).toBe(true);
  noteToolCallShownInFlight(session, "new");
  expect(wasToolCallShownInFlight(session, "1")).toBe(false);
  expect(wasToolCallShownInFlight(session, "0")).toBe(true);
  expect(wasToolCallShownInFlight(session, "new")).toBe(true);
});

it("keeps eight recent sessions and isolates matching call IDs", () => {
  const session = (index: number) => `session-capacity-${index}`;
  for (let index = 0; index < 8; index++) noteToolCallShownInFlight(session(index), "shared");
  expect(wasToolCallShownInFlight(session(8), "shared")).toBe(false);
  expect(wasToolCallShownInFlight(session(0), "shared")).toBe(true);
  noteToolCallShownInFlight(session(8), "shared");
  expect(wasToolCallShownInFlight(session(1), "shared")).toBe(false);
  expect(wasToolCallShownInFlight(session(0), "shared")).toBe(true);
  expect(wasToolCallShownInFlight(session(8), "shared")).toBe(true);
});
