import { expect, it } from "vitest";
import type { ToolPartView } from "./tool-part-model.ts";
import { searchToolPart, toolPartFindCorpus } from "./tool-part-find.ts";

it("keeps each folded call searchable with its own reveal identity", async () => {
 const source = (id:string,text:string):ToolPartView => ({ id,toolCallId:id,assistantMessageId:"assistant",messageId:"result",tool:"command",kind:"command",status:"completed",args:{command:text} });
 const first=source("first","cat first.rs");
 const second=source("second","cat second.rs");
 const row:ToolPartView={...first,commandActivity:{fact:{id:"activity",label:"Read files",subject:"files",group:"Exploring files",outcome:"Completed",state:"completed",execution_ids:[],spans:[]},members:[],calls:[first,second]}};
 expect(toolPartFindCorpus(row)).toContain("second.rs");
 const matches=await searchToolPart(row,undefined,"second.rs",true,new AbortController().signal);
 expect(matches).toHaveLength(1);
 expect(matches[0]!.toolCallId).toBe("second");
});
