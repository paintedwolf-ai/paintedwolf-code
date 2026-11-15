import { afterEach, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { searchToolPart, toolPartFindCorpus } from "./tool-part-find.ts";

afterEach(() => setLycaonClientForTest(null));

it("keeps each folded call searchable with its own reveal identity", async () => {
 const source = (id:string,text:string):ToolPartView => ({ id,toolCallId:id,assistantMessageId:"assistant",messageId:"result",tool:"command",kind:"command",status:"completed",args:{command:text} });
 const first=source("first","cat first.rs");
 const second=source("second","cat second.rs");
 const row:ToolPartView={...first,commandActivity:{fact:{id:"activity",label:"Read files",subject:"files",group:"Exploring files",phase:"inspect",outcome:"Completed",state:"completed",execution_ids:[],spans:[]},members:[],calls:[first,second],receiptScopes:[]}};
 expect(toolPartFindCorpus(row)).toContain("second.rs");
 const matches=await searchToolPart(row,undefined,"second.rs",true,new AbortController().signal);
 expect(matches).toHaveLength(1);
 expect(matches[0]!.toolCallId).toBe("second");
});

it("pages retained tool content and retries once when a cursor expires", async () => {
  let attempt = 0;
  const searchChatContent = vi.fn(async (_s: string, _m: string, _r: unknown, _q: string, cursor?: string) => {
    attempt++;
    if (attempt === 1) return { matches: [{ offset: 2, length: 4 }], next_cursor: "c1" };
    if (attempt === 2 && cursor === "c1") throw new LycaonApiError("expired", 409, "cursor_generation_expired");
    if (attempt === 3) return { matches: [{ offset: 2, length: 4 }], next_cursor: "c2" };
    return { matches: [{ offset: 10, length: 4 }], next_cursor: "" };
  });
  setLycaonClientForTest(stubClient({ searchChatContent }));
  const part: ToolPartView = {
    id: "p1", toolCallId: "c1", assistantMessageId: "a1", messageId: "m1", tool: "read", kind: "read", status: "completed",
    args: {}, outputReference: { field: "tool_output", sha256: "h", rows: 2, total_runes: 20, size_bytes: 20, preview_rows: [] },
  };
  const matches = await searchToolPart(part, "sess-1", "test", true, new AbortController().signal);
  expect(matches).toEqual([
    { from: 2, to: 6, field: "tool_output" },
    { from: 10, to: 14, field: "tool_output" },
  ]);
  expect(searchChatContent).toHaveBeenCalledTimes(4);

  setLycaonClientForTest(
    stubClient({
      searchChatContent: () => Promise.reject(new Error("boom")),
    }),
  );
  await expect(searchToolPart(part, "sess-1", "test", true, new AbortController().signal)).rejects.toThrow("boom");
});
