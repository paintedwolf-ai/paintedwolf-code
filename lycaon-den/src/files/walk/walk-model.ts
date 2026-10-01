import { effectContributors, contributorsLabel } from "../review/review-model.ts";
import type {
  SourceCommandWindow,
  SourceGitChange,
  SourceWalkEffect,
  SourceWalkFile,
  SourceWalkTurn,
} from "../../api/types.ts";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import { buildWalkChapters, type WalkChapter } from "./walk-chapters.ts";
import {
  commandWindowAction,
} from "../source/source-command-window.ts";
import { gitChangeAction, gitChangeLine, gitChangeTitle } from "../source/source-git-change.ts";

export type WalkEffectStep = {
  kind: "effect";
  /** Stable rail identity across refreshes. */
  key: string;
  ordinal: number;
  effect: SourceWalkEffect;
  toolCallId: string | null;
  label: string;
};

export type WalkGitStep = {
  kind: "git";
  key: string;
  ordinal: number;
  change: SourceGitChange;
  /** Effects the movement rewrote, oldest first. */
  effects: readonly SourceWalkEffect[];
  toolCallId: string | null;
  label: string;
};

export type WalkCommandStep = {
  kind: "command";
  key: string;
  ordinal: number;
  command: SourceCommandWindow;
  /** Effects observed inside the window, oldest first. */
  effects: readonly SourceWalkEffect[];
  /** Links the step to its command in the transcript. */
  toolCallId: string | null;
  label: string;
};

/** Unaffiliated changes between two session steps. */
export type WalkOutsideStep = {
  kind: "outside";
  key: string;
  ordinal: number;
  /** Effects observed outside the app, oldest first. */
  effects: readonly SourceWalkEffect[];
  toolCallId: null;
  label: string;
};

export type WalkGroupStep = WalkGitStep | WalkCommandStep | WalkOutsideStep;

export type WalkStep = WalkEffectStep | WalkGroupStep;

export type Walk = {
  baseline: string;
  steps: readonly WalkStep[];
  chapters: readonly WalkChapter[];
};

export type WalkFileTarget = {
  rootId: string;
  path: string;
};

type WalkStepKind = "create" | "write" | "delete" | "git" | "command" | "outside";

export const EMPTY_WALK: Walk = { baseline: "", steps: [], chapters: [] };

function trimmed(value: string | undefined | null): string {
  return value?.trim() ?? "";
}

function collectWalkEffects(
  files: readonly SourceWalkFile[],
): SourceWalkEffect[] {
  const effects: SourceWalkEffect[] = [];
  for (const file of files) {
    effects.push(...file.effects);
  }
  effects.sort((a, b) => {
    const byOrd = a.ordinal - b.ordinal;
    return byOrd !== 0 ? byOrd : a.id.localeCompare(b.id);
  });
  return effects;
}

function effectRailLabel(effect: SourceWalkEffect): string {
  const toolName = trimmed(effect.tool_name);
  if (toolName) return toolName;
  if (effect.origin === "user") return "edit";
  const cause = trimmed(effect.cause);
  if (cause) return cause.replaceAll("_", " ");
  return effect.op;
}

function walkGitStepKey(changeId: string): string {
  return `git:${changeId}`;
}

function walkCommandStepKey(commandId: string): string {
  return `command:${commandId}`;
}

function walkOutsideStepKey(firstEffectId: string): string {
  return `outside:${firstEffectId}`;
}

/** Outside-origin effects without session affiliation. */
function isOutsideEffect(effect: SourceWalkEffect): boolean {
  return effect.contributors.every((author) => author.origin === "external" && !author.session_id);
}

/** Groups consecutive outside effects; single edits remain individual steps. */
function collapseOutsideRuns(steps: readonly WalkStep[]): WalkStep[] {
  const out: WalkStep[] = [];
  let run: WalkEffectStep[] = [];
  const flush = () => {
    if (run.length === 1) {
      out.push(run[0]!);
    } else if (run.length > 1) {
      const first = run[0]!;
      out.push({
        kind: "outside",
        key: walkOutsideStepKey(first.effect.id),
        ordinal: first.ordinal,
        effects: run.map((step) => step.effect),
        toolCallId: null,
        label: "outside",
      });
    }
    run = [];
  };
  for (const step of steps) {
    if (step.kind === "effect" && isOutsideEffect(step.effect)) {
      run.push(step);
      continue;
    }
    flush();
    out.push(step);
  }
  flush();
  return out;
}

export function buildWalk(
  baseline: string,
  files: readonly SourceWalkFile[],
  gitChanges: readonly SourceGitChange[] = [],
  commands: readonly SourceCommandWindow[] = [],
  turns: readonly SourceWalkTurn[] = [],
): Walk {
  const knownGit = new Map<string, SourceGitChange>();
  for (const change of gitChanges) {
    if (!knownGit.has(change.id)) knownGit.set(change.id, change);
  }
  const knownCommands = new Map<string, SourceCommandWindow>();
  for (const command of commands) {
    if (!knownCommands.has(command.id)) knownCommands.set(command.id, command);
  }
  const byGit = new Map<string, SourceWalkEffect[]>();
  const byCommand = new Map<string, SourceWalkEffect[]>();
  const steps: WalkStep[] = [];
  for (const effect of collectWalkEffects(files)) {
    // Repository movements take precedence over command windows.
    const changeId = trimmed(effect.git_change_id);
    if (changeId && knownGit.has(changeId)) {
      const held = byGit.get(changeId);
      if (held) held.push(effect);
      else byGit.set(changeId, [effect]);
      continue;
    }
    const commandId = trimmed(effect.command_id);
    if (commandId && knownCommands.has(commandId)) {
      const held = byCommand.get(commandId);
      if (held) held.push(effect);
      else byCommand.set(commandId, [effect]);
      continue;
    }
    const callId = trimmed(effect.tool_call_id);
    steps.push({
      kind: "effect",
      key: effect.id,
      ordinal: effect.ordinal,
      effect,
      toolCallId: callId || null,
      label: effectRailLabel(effect),
    });
  }
  for (const change of knownGit.values()) {
    steps.push({
      kind: "git",
      key: walkGitStepKey(change.id),
      ordinal: change.ordinal,
      change,
      effects: byGit.get(change.id) ?? [],
      toolCallId: trimmed(change.tool_call_id) || null,
      label: gitChangeAction(change),
    });
  }
  // Only command windows named by an effect become steps.
  for (const command of knownCommands.values()) {
    const effects = byCommand.get(command.id);
    if (!effects) continue;
    steps.push({
      kind: "command",
      key: walkCommandStepKey(command.id),
      ordinal: command.ordinal,
      command,
      effects,
      toolCallId: trimmed(command.tool_call_id) || null,
      label: commandWindowAction(command),
    });
  }
  steps.sort((a, b) => {
    const byOrd = a.ordinal - b.ordinal;
    return byOrd !== 0 ? byOrd : a.key.localeCompare(b.key);
  });
  const collapsed = collapseOutsideRuns(steps);
  return { baseline: trimmed(baseline), steps: collapsed, chapters: buildWalkChapters(collapsed, turns) };
}

/** Renames use the write treatment. */
export function walkStepKind(step: WalkStep): WalkStepKind {
  if (step.kind !== "effect") return step.kind;
  switch (step.effect.op) {
    case "create":
      return "create";
    case "delete":
      return "delete";
    case "rename":
    case "write":
      return "write";
  }
}

export function walkStepOperation(step: WalkStep): string {
  if (step.kind === "git") return gitChangeTitle(step.change);
  if (step.kind === "command") return "Command";
  if (step.kind === "outside") return "Outside the app";
  switch (step.effect.op) {
    case "create":
      return "Created";
    case "delete":
      return "Deleted";
    case "rename":
      return "Renamed";
    case "write":
      return "Modified";
  }
}

export function walkGroupGlyph(step: WalkGroupStep): string {
  switch (step.kind) {
    case "git":
      return "⎇";
    case "command":
      return "$";
    case "outside":
      return "◇";
  }
}

function walkEffectActor(effect: SourceWalkEffect): string {
  return contributorsLabel(effectContributors(effect));
}

export function walkStepActor(step: WalkStep): string {
  if (step.kind === "git") return "Git";
  if (step.kind === "command") return "Command";
  if (step.kind === "outside") return "Outside the app";
  return walkEffectActor(step.effect);
}

export function walkStepAction(step: WalkStep): string {
  return step.label.trim().replaceAll("_", " ");
}

export function walkStepGitChange(step: WalkStep): string | null {
  if (step.kind !== "git") return null;
  return gitChangeLine(step.change);
}

/** Group labels distinguish committed content from observed working-tree files. */
export function walkGroupFileSummary(step: WalkGroupStep): string {
  const count = walkStepFiles(step).length;
  if (step.kind === "git") return count === 0 ? "Git review" : `${count} observed ${count === 1 ? "file" : "files"}`;
  if (count === 0) return "No file changes";
  return `${count} ${count === 1 ? "file" : "files"}`;
}

/** Observed runs use a single timestamp or a time span. */
export function walkObservedSpan(effects: readonly SourceWalkEffect[]): string {
  const first = relativeTimeLabel(effects[0]?.observed_at);
  const last = relativeTimeLabel(effects[effects.length - 1]?.observed_at);
  if (!first) return "";
  return last && last !== first ? `${first} to ${last}` : first;
}

export function walkStepEffects(step: WalkStep): readonly SourceWalkEffect[] {
  return step.kind === "effect" ? [step.effect] : step.effects;
}

/** Latest effect per file, ordered by path. */
export function walkStepFiles(step: WalkStep): SourceWalkEffect[] {
  const latest = new Map<string, SourceWalkEffect>();
  for (const effect of walkStepEffects(step)) {
    const key = trimmed(effect.file_id) || `${effect.root_id}\0${effect.path}`;
    const held = latest.get(key);
    if (!held || effect.ordinal > held.ordinal) latest.set(key, effect);
  }
  return [...latest.values()].sort((a, b) => a.path.localeCompare(b.path));
}

/** The effect an effect step presents; a group step presents its page. */
export function walkStepEffectForVersion(
  step: WalkStep,
  versionId: string,
): SourceWalkEffect | null {
  const wanted = versionId.trim();
  if (!wanted || step.kind !== "effect") return null;
  return step.effect.after_version_id === wanted ? step.effect : null;
}

export function walkStepAt(walk: Walk, index: number): WalkStep | null {
  return walk.steps[index] ?? null;
}

function effectAddresses(
  effect: SourceWalkEffect,
  rootId: string,
  path: string,
): boolean {
  return (
    (effect.root_id === rootId && effect.path === path) ||
    (effect.from_root_id === rootId && effect.from_path === path)
  );
}

/** The first step touching the file, by its current tree path. */
export function walkStepIndexForFile(
  walk: Walk,
  target: WalkFileTarget | null | undefined,
): number {
  const rootId = target?.rootId.trim() ?? "";
  const path = target?.path.trim() ?? "";
  if (!rootId || !path) return -1;
  let addressedEffect: SourceWalkEffect | null = null;
  for (const step of walk.steps) {
    addressedEffect =
      walkStepEffects(step).find((effect) =>
        effectAddresses(effect, rootId, path)
      ) ?? null;
    if (addressedEffect) break;
  }
  if (!addressedEffect) return -1;
  const fileId = addressedEffect.file_id.trim();
  return walk.steps.findIndex((step) =>
    fileId
      ? walkStepEffects(step).some((effect) => effect.file_id === fileId)
      : walkStepEffects(step).some((effect) => effectAddresses(effect, rootId, path))
  );
}

export function clampWalkIndex(walk: Walk, index: number): number {
  if (walk.steps.length === 0) return -1;
  if (!Number.isFinite(index)) return walk.steps.length - 1;
  return Math.max(0, Math.min(walk.steps.length - 1, Math.trunc(index)));
}
