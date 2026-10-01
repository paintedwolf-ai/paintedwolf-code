import { CONTEXT_NAV_CATALOG, type RecentSession } from "../../../shared/app-state-types.ts";
import type { Project } from "../../api/types.ts";
import type { AttentionIndex } from "../../attention/attention-model.ts";
import { sessionTitle } from "../../chat/session/session-title.ts";
import type { CrossbarGotoTarget } from "../../search/crossbar-model.ts";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../settings/security/project-settings-overlay-copy.ts";
import { SETTINGS_REGISTRY, settingContext } from "../../settings/settings-registry.ts";
import { availabilityFromProject, STAGE_REGISTRY } from "../stage/stage-registry.tsx";

// Settings targets; Crossbar shows them only for a typed query.
const settingGotoTargets: CrossbarGotoTarget[] = SETTINGS_REGISTRY.map(
  (setting) => ({
    kind: "setting",
    settingId: setting.id,
    label: setting.label,
    context: settingContext(setting),
    keywords: setting.keywords,
  }),
);

export function shellSearchTargets(projects: readonly Project[], recentSessions: readonly RecentSession[],
  attention: AttentionIndex, active: Project | null | undefined, securityScannersEnabled: () => boolean): CrossbarGotoTarget[] {
  const nameById = new Map(
    projects.map((p) => [p.id, p.name?.trim() || undefined] as const),
  );
  const targets: CrossbarGotoTarget[] = [];
  const recents = [...recentSessions].sort(
    (a, b) => (b.lastActivityAt ?? 0) - (a.lastActivityAt ?? 0),
  );
  for (const row of recents) {
    targets.push({
      kind: "session",
      projectId: row.projectId,
      sessionId: row.sessionId,
      label: sessionTitle(row.title),
      context: nameById.get(row.projectId),
      // Missing attention means idle.
      attention: attention.get(row.sessionId),
    });
  }
  for (const project of projects) {
    targets.push({
      kind: "project",
      projectId: project.id,
      label: project.name?.trim() || "Untitled project",
      context: project.roots[0]?.label ?? undefined,
    });
  }
  if (active) {
    // Project configuration is a shell pane, not a registry stage.
    targets.push({
      kind: "surface",
      surface: "context",
      label: PROJECT_SETTINGS_OVERLAY_COPY.stageTitle,
      context: active.name ?? undefined,
    });
    // Registry metadata defines available stage targets.
    const availability = availabilityFromProject({
      project: active,
      securityScannersEnabled: securityScannersEnabled(),
    });
    for (const stageId of CONTEXT_NAV_CATALOG) {
      const definition = STAGE_REGISTRY[stageId];
      if (!definition.available(availability)) continue;
      targets.push({
        kind: "surface",
        surface: stageId,
        label: definition.label,
        context: active.name ?? undefined,
      });
    }
  }
  // Shell-level destinations exist with or without a project.
  targets.push(
    { kind: "surface", surface: "chats", label: "All chats" },
    { kind: "surface", surface: "home", label: "Home" },
    { kind: "surface", surface: "settings", label: "Settings" },
    ...settingGotoTargets,
  );
  return targets;
}
