import { expect, it, vi } from "vitest";
import { selectedReaderText } from "./source-reader-selection.ts";
import { sourceReaderFixture } from "../../../test/source-reader-fixture.ts";
import { sourceReaderAccess } from "../../../api/source-reader.ts";
import { stubClient } from "../../../test/client-fixture.ts";

it("copies the unloaded text between source selection endpoints", async () => {
  const fixture = sourceReaderFixture();
  const reference = fixture.prepare("first\nmiddle\nlast\n", "first\nmiddle\nlast\n");
  const rows = vi.fn(fixture.getSourceViewRows);
  const access = sourceReaderAccess(stubClient({ ...fixture.methods, getSourceViewRows: rows }), "project", reference);
  expect(await selectedReaderText(access, { section: "", row: 0, offset: 2, side: "after" }, { section: "", row: 2, offset: 2, side: "after" })).toBe("rst\nmiddle\nla");
  expect(rows).toHaveBeenCalledOnce();
});
