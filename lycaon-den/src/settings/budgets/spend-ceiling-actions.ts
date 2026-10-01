import type { LycaonClient } from "../../api/client.ts";
import type { SettingsLimitsResponse } from "../../api/types.ts";
import { NANO_PER_USD } from "../../cost/nano-usd.ts";
import { raisedSpendCeilingUsd } from "./spend-ceiling-readout.ts";

export type SpendCeilingUpdate = {
  scope: "global" | "project";
  limits: SettingsLimitsResponse;
};

/** Raises the guardrail that constrains the session. */
export async function raiseEffectiveSpendCeiling(
  client: LycaonClient,
  projectId: string,
  spentUsd: number,
): Promise<SpendCeilingUpdate> {
  const global = await client.getLimitsSettings();
  const project = await client.getLimitsSettings(projectId);
  const globalCeiling = (global.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  const globalArmed = global.spend_ceiling_enabled === true && globalCeiling > 0;
  const projectCeiling = (project.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  const projectCeilingIsEffective =
    project.spend_ceiling_enabled === true &&
    projectCeiling > 0 &&
    (!globalArmed || projectCeiling < globalCeiling);

  if (projectCeilingIsEffective && (!globalArmed || spentUsd < globalCeiling)) {
    const raised = raisedSpendCeilingUsd(projectCeiling, spentUsd);
    const next = globalArmed ? Math.min(raised, globalCeiling) : raised;
    if (next > spentUsd) {
      return {
        scope: "project",
        limits: await client.updateLimitsSettings(
          {
            spend_ceiling_enabled: true,
            session_spend_ceiling_nano_usd: Math.round(next * NANO_PER_USD),
          },
          projectId,
        ),
      };
    }
  }

  const current = (global.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  const limits = await client.updateLimitsSettings(
    {
      spend_ceiling_enabled: true,
      session_spend_ceiling_nano_usd: Math.round(raisedSpendCeilingUsd(current, spentUsd) * NANO_PER_USD),
    },
  );
  // An explicit project ceiling can remain exhausted after the device limit rises.
  const effective = await client.getLimitsSettings(projectId);
  const effectiveCeiling = (effective.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  const globalLimitUsd = (limits.session_spend_ceiling_nano_usd ?? 0) / NANO_PER_USD;
  if (effective.spend_ceiling_enabled && effectiveCeiling > 0 && effectiveCeiling <= spentUsd) {
    await client.updateLimitsSettings(
      {
        spend_ceiling_enabled: true,
        session_spend_ceiling_nano_usd: Math.round(Math.min(
          raisedSpendCeilingUsd(effectiveCeiling, spentUsd),
          globalLimitUsd,
        ) * NANO_PER_USD),
      },
      projectId,
    );
  }
  return { scope: "global", limits };
}
