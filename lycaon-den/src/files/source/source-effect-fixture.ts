import type { SourceWalkEffect } from "../../api/types.ts";
import { TEST_OWNER_PERSON_ID } from "../../platform/connection/host-identity-test.ts";

/** A single-author publication unless the fixture supplies mixed authors. */
export function sourceEffectFixture(effect: Omit<SourceWalkEffect, "contributors"> & Partial<Pick<SourceWalkEffect, "contributors">>): SourceWalkEffect {
  return { ...effect, contributors: effect.contributors ?? [{
    origin: effect.origin, session_id: effect.session_id ?? "", turn: effect.turn,
    person_id: effect.origin === "user" ? TEST_OWNER_PERSON_ID : "", actor_label: effect.actor_label ?? "",
    tool_call_id: effect.tool_call_id ?? "", tool_name: effect.tool_name ?? "", worker_id: effect.worker_id ?? "",
  }] };
}
