import type { JSX } from "solid-js";
import type { Project, ProjectRoot, WorkflowSummary } from "../../api/types.ts";
import type { FileBufferKey } from "../../files/components/project-files-model.ts";
import type {
  FileTabWindowDrag,
  ItemWindowDropPosition,
} from "../../platform/windows/item-windows.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import type { ContextNavItemId } from "../../../shared/app-state-types.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../settings/settings-nav-model.ts";
import type { SettingsSection } from "../../settings/settings-nav-model.ts";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { ProjectFilesView } from "../../files/components/ProjectFilesView.tsx";
import { ProjectArtifactsView } from "../project/ProjectArtifactsView.tsx";
import { ProjectBlueprintsView } from "../project/ProjectBlueprintsView.tsx";
import { ProjectExtensionsView } from "../project/ProjectExtensionsView.tsx";
import { ProjectScansView } from "../scan/ProjectScansView.tsx";
import { ProjectCostView } from "../project/ProjectCostView.tsx";
import { GlobalSearchView } from "../search/GlobalSearchView.tsx";
import type { SearchNavTarget } from "../../search/search-hit-nav.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import {
  StageArtifactsIcon,
  StageBlueprintsIcon,
  StageCostIcon,
  StageExtensionsIcon,
  StageFilesIcon,
  StageSearchIcon,
  StageSecurityIcon,
} from "./stage-icons.tsx";

export type StageFeatureFlags = {
  securityScannersEnabled: boolean;
};

export type StageAvailability = StageFeatureFlags & {
  projectExists: boolean;
  roots: readonly ProjectRoot[];
};

export type StageRenderCallbacks = {
  onOpenSession: (args: { projectId: string; sessionId: string }) => void;
  onOpenCreatedSession: (args: { projectId: string; sessionId: string }) => void;
  onCreateSupportingWorkflowSession: (args: {
    projectId: string;
    workflow: WorkflowSummary;
  }) => Promise<void>;
  onOpenSettings: (section: SettingsSection) => void;
  onSearchNavigate: (target: SearchNavTarget) => void;
  onCloseTabIdle?: () => void;
  onOpenInNewWindow?: (
    bufferKey?: FileBufferKey,
    position?: ItemWindowDropPosition,
  ) => void | Promise<void>;
  onOpenFileInNewWindow?: (
    rootId: string,
    path: string,
    title: string,
  ) => void | Promise<void>;
  fileTabWindowDrag?: FileTabWindowDrag;
  onFileTabMovedOut?: (bufferKey: string) => void | Promise<void>;
  /** File-addressed peers start with one tab. */
  restoreFilesHotExit?: boolean;
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
  onOpenSearch?: (args: {
    seed: string;
    originProjectId: string | null;
  }) => void;
};

export type StageRenderContext = {
  projectId: string | null;
  projectName: string;
  appStore: AppStore;
  roots: readonly ProjectRoot[];
  projects: readonly Project[];
  back: () => StageBack | null;
  search?: {
    originProjectId: string | null;
    seed?: string;
    replaceMode?: boolean;
    serial: number;
  };
  callbacks: StageRenderCallbacks;
};

export type StageDefinition = {
  id: ContextNavItemId;
  label: string;
  navTestId: string;
  icon: () => JSX.Element;
  backPlacement: "titlebar" | "stage";
  featureEnabled: (flags: StageFeatureFlags) => boolean;
  /** Includes runtime availability beyond feature flags. */
  available: (ctx: StageAvailability) => boolean;
  render: (ctx: StageRenderContext) => JSX.Element;
};

export const STAGE_REGISTRY: Record<ContextNavItemId, StageDefinition> = {
  files: {
    id: "files",
    label: "Files",
    navTestId: "project-files-entry",
    icon: StageFilesIcon,
    backPlacement: "titlebar",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectFilesView
          projectId={projectId}
          projectName={ctx.projectName}
          appStore={ctx.appStore}
          roots={[...ctx.roots]}
          client={getLycaonClient()}
          onOpenInNewWindow={ctx.callbacks.onOpenInNewWindow}
          onOpenFileInNewWindow={ctx.callbacks.onOpenFileInNewWindow}
          fileTabWindowDrag={ctx.callbacks.fileTabWindowDrag}
          onFileTabMovedOut={ctx.callbacks.onFileTabMovedOut}
          restoreHotExit={ctx.callbacks.restoreFilesHotExit}
          onCloseTabIdle={ctx.callbacks.onCloseTabIdle}
          onRevealInTranscript={ctx.callbacks.onRevealInTranscript}
          onOpenSearch={ctx.callbacks.onOpenSearch}
        />
      );
    },
  },
  search: {
    id: "search",
    label: "Search",
    navTestId: "project-search-entry",
    icon: StageSearchIcon,
    backPlacement: "stage",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const originProjectId = ctx.search?.originProjectId ?? ctx.projectId;
      const originName =
        originProjectId
          ? (ctx.projects.find((p) => p.id === originProjectId)?.name?.trim() ||
              ctx.projectName ||
              null)
          : null;
      return (
        <GlobalSearchView
          originProjectId={originProjectId}
          originName={originName}
          seed={ctx.search?.seed}
          seedSerial={ctx.search?.serial}
          replaceModeRequested={ctx.search?.replaceMode}
          appStore={ctx.appStore}
          projects={ctx.projects}
          back={ctx.back()}
          onNavigate={ctx.callbacks.onSearchNavigate}
        />
      );
    },
  },
  security: {
    id: "security",
    label: SECURITY_SCANNERS_SECTION_LABEL,
    navTestId: "project-security-entry",
    icon: StageSecurityIcon,
    backPlacement: "stage",
    featureEnabled: (flags) => flags.securityScannersEnabled,
    // Scanning is device-scoped; trust controls reporting.
    available: (ctx) => ctx.projectExists && ctx.securityScannersEnabled,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectScansView
          projectId={projectId}
          appStore={ctx.appStore}
          back={ctx.back()}
          roots={ctx.roots}
          repoRoot={
            ctx.roots.find((r) => r.is_primary)?.path ?? ctx.roots[0]?.path
          }
        />
      );
    },
  },
  cost: {
    id: "cost",
    label: "Cost",
    navTestId: "project-cost-entry",
    icon: StageCostIcon,
    backPlacement: "stage",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectCostView
          projectId={projectId}
          projectName={ctx.projectName}
          appStore={ctx.appStore}
          back={ctx.back()}
          onOpenSession={(sessionId) =>
            ctx.callbacks.onOpenSession({ projectId, sessionId })
          }
          onOpenCostSettings={() => ctx.callbacks.onOpenSettings("cost")}
        />
      );
    },
  },
  artifacts: {
    id: "artifacts",
    label: "Artifacts",
    navTestId: "project-artifacts-entry",
    icon: StageArtifactsIcon,
    backPlacement: "stage",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectArtifactsView
          projectId={projectId}
          appStore={ctx.appStore}
          back={ctx.back()}
          onRevealInTranscript={ctx.callbacks.onRevealInTranscript}
        />
      );
    },
  },
  blueprints: {
    id: "blueprints",
    label: "Blueprints",
    navTestId: "project-blueprints-entry",
    icon: StageBlueprintsIcon,
    backPlacement: "stage",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectBlueprintsView
          projectId={projectId}
          appStore={ctx.appStore}
          back={ctx.back()}
          hasRepo={ctx.roots.length > 0}
          roots={ctx.roots}
          onLaunchedSession={(target) =>
            ctx.callbacks.onOpenCreatedSession({
              projectId,
              sessionId: target.sessionId,
            })
          }
          onCreateSupportingSession={(workflow) =>
            ctx.callbacks.onCreateSupportingWorkflowSession({ projectId, workflow })
          }
        />
      );
    },
  },
  extensions: {
    id: "extensions",
    label: "Extensions",
    navTestId: "project-extensions-entry",
    icon: StageExtensionsIcon,
    backPlacement: "stage",
    featureEnabled: () => true,
    available: (ctx) => ctx.projectExists,
    render: (ctx) => {
      const projectId = ctx.projectId;
      if (!projectId) return <></>;
      return (
        <ProjectExtensionsView
          projectId={projectId}
          appStore={ctx.appStore}
          back={ctx.back()}
          onOpenScanners={() => ctx.callbacks.onOpenSettings("scanners")}
        />
      );
    },
  },
};

export function stageLabelFor(id: ContextNavItemId): string {
  return STAGE_REGISTRY[id].label;
}

export function stageDefinition(id: ContextNavItemId): StageDefinition {
  return STAGE_REGISTRY[id];
}

export function stageFeatureEnabled(
  id: ContextNavItemId,
  flags: StageFeatureFlags,
): boolean {
  return STAGE_REGISTRY[id].featureEnabled(flags);
}

export function availabilityFromProject(args: {
  project: Pick<Project, "roots_generation" | "roots"> | null;
  securityScannersEnabled: boolean;
}): StageAvailability {
  if (!args.project) {
    return {
      projectExists: false,
      securityScannersEnabled: args.securityScannersEnabled,
      roots: [],
    };
  }
  return {
    projectExists: true,
    securityScannersEnabled: args.securityScannersEnabled,
    roots: args.project.roots ?? [],
  };
}
