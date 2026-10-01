import type { SourceWalkTurn } from "../../api/types.ts";
import type { Walk, WalkStep } from "./walk-model.ts";

export type WalkChapter = {
  key: string;
  sessionId: string;
  turn: number;
  messageId: string;
  title: string;
  prompt: string;
  stepKeys: readonly string[];
};

const ACTIVITY = "activity";

function walkTurnKey(sessionId: string, turn: number): string {
  return `${sessionId}\0${turn}`;
}

/** The chapter a step names on its own, from its recorded turn. */
function walkStepChapterKey(step: WalkStep): string {
  if (step.kind === "command" || step.kind === "git") {
    const source = step.kind === "command" ? step.command : step.change;
    if (source.session_id && source.turn) return walkTurnKey(source.session_id, source.turn);
    if (step.kind === "command") return ACTIVITY;
  }
  const effects = step.kind === "effect" ? [step.effect] : step.effects;
  const first = effects[0];
  if (!first?.session_id || first.turn < 1) return ACTIVITY;
  if (effects.some((effect) => effect.session_id !== first.session_id || effect.turn !== first.turn)) {
    return ACTIVITY;
  }
  return walkTurnKey(first.session_id, first.turn);
}

// Unaffiliated steps join a turn when the same turn borders both sides.
function walkChapterKeys(steps: readonly WalkStep[]): string[] {
  const chapterKeys = steps.map(walkStepChapterKey);
  const keys = [...chapterKeys];
  let index = 0;
  while (index < keys.length) {
    if (chapterKeys[index] !== ACTIVITY) {
      index += 1;
      continue;
    }
    let end = index;
    while (end < keys.length && chapterKeys[end] === ACTIVITY) end += 1;
    const before = index > 0 ? chapterKeys[index - 1] : "";
    const after = end < chapterKeys.length ? chapterKeys[end] : "";
    if (before && before === after) {
      for (let at = index; at < end; at += 1) keys[at] = before;
    }
    index = end;
  }
  return keys;
}

export function buildWalkChapters(
  steps: readonly WalkStep[],
  turns: readonly SourceWalkTurn[],
): WalkChapter[] {
  const descriptions = new Map(turns.map((turn) => [walkTurnKey(turn.session_id, turn.turn), turn]));
  const chapters = new Map<string, WalkChapter & { stepKeys: string[] }>();
  const keys = walkChapterKeys(steps);
  for (const [index, step] of steps.entries()) {
    const key = keys[index]!;
    let chapter = chapters.get(key);
    if (!chapter) {
      const source = step.kind === "command" ? step.command
        : step.kind === "git" && step.change.session_id ? step.change
        : step.kind === "effect" ? step.effect : step.effects[0];
      const metadata = descriptions.get(key);
      const turn = key === ACTIVITY ? 0 : metadata?.turn ?? source?.turn ?? 0;
      chapter = {
        key,
        sessionId: key === ACTIVITY ? "" : metadata?.session_id ?? source?.session_id ?? "",
        turn,
        messageId: metadata?.message_id ?? "",
        title: turn > 0 ? `Turn ${turn}` : "Source activity",
        prompt: metadata?.prompt ?? "",
        stepKeys: [],
      };
      chapters.set(key, chapter);
    }
    chapter.stepKeys.push(step.key);
  }
  return [...chapters.values()];
}

export function walkChapterAt(walk: Walk, index: number): WalkChapter | null {
  const step = walk.steps[index];
  return step ? walk.chapters.find((chapter) => chapter.stepKeys.includes(step.key)) ?? null : null;
}

export function walkChapterStart(walk: Walk, chapter: WalkChapter): number {
  return walk.steps.findIndex((step) => step.key === chapter.stepKeys[0]);
}

export function latestWalkTurnStart(walk: Walk): number {
  const chapter = walk.chapters.filter((value) => value.turn > 0)
    .reduce<WalkChapter | null>((latest, value) => !latest || value.turn > latest.turn ? value : latest, null);
  return chapter ? walkChapterStart(walk, chapter) : walk.steps.length > 0 ? 0 : -1;
}

export function walkChapterSegments(walk: Walk): { key: string; title: string; start: number; end: number }[] {
  const segments: { key: string; title: string; start: number; end: number }[] = [];
  const titles = new Map(walk.chapters.map((chapter) => [chapter.key, chapter.title]));
  const keys = walkChapterKeys(walk.steps);
  let previous = "";
  for (const [index, step] of walk.steps.entries()) {
    const chapterKey = keys[index]!;
    if (chapterKey === previous) segments[segments.length - 1]!.end = index;
    else segments.push({ key: step.key, title: titles.get(chapterKey) ?? "Source activity", start: index, end: index });
    previous = chapterKey;
  }
  return segments;
}
