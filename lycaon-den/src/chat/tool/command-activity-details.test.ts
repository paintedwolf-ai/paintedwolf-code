import { expect, it } from "vitest";
import type { CommandExecutionReceipt } from "../../api/types.ts";
import type { CommandActivityView } from "./command-activity-model.ts";
import { commandActivityFacts } from "./command-activity-details.ts";

const view: CommandActivityView = { fact:{ id:"activity",group:"Changing files",label:"Change files",subject:"file.rs",state:"waiting",outcome:"Waiting",job_handle:"job",directory:"/project",execution_ids:["execution"],spans:[] },members:[],calls:[] };

it("keeps uncertainty and recovery facts attached to their actual executions", () => {
 const row={command:"write file.rs",effect_certainty:"unknown",state:"waiting",owner_started:true,owner_invoked:false,approval_ref:"approval",evidence_ref:"captured:file@r1",stop_code:"REFUSED",host_stop_code:"HOST_REFUSED",accepted_revision:"r2",held_version_id:"held",recovery_ref:"recovery",revision_conflict:{path:"file.rs",expected_revision:"r1",observed_revision:"r3"}} as CommandExecutionReceipt;
 const facts=commandActivityFacts(view,[row], [{code:"HOST_REFUSED",details:{native_operation:"write"}}]);
 expect(facts).toContainEqual({label:"Effects",value:"Unknown outcome"});
 expect(facts).toContainEqual({label:"Publication",value:"Accepted, not published"});
 expect(facts).toContainEqual({label:"Observed revision",value:"r3"});
 expect(facts).toContainEqual({label:"Recovery",value:"recovery"});
 expect(facts).toContainEqual({label:"Evidence",value:"captured:file@r1"});
 expect(facts).toContainEqual({label:"Owner",value:"Started, invocation unknown"});
 expect(facts).toContainEqual({label:"Operation",value:"write"});
});

it("reports partial effects without calling unknown effects completed", () => {
 const rows=[{command:"write",effect_certainty:"completed",state:"completed",published_revision:"r2"},{command:"upload",effect_certainty:"attempted",state:"failed"}] as CommandExecutionReceipt[];
 expect(commandActivityFacts(view,rows)).toContainEqual({label:"Effects",value:"Partial effects; unknown outcome"});
 expect(commandActivityFacts(view,[]).some(fact=>fact.label==="Effects")).toBe(false);
});
