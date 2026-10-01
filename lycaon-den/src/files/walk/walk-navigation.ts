import { commandWindowLine } from "../source/source-command-window.ts";
import { gitChangeLine } from "../source/source-git-change.ts";
import { walkChapterSegments } from "./walk-chapters.ts";
import { walkGroupFileSummary, walkStepFiles, walkStepOperation, type Walk } from "./walk-model.ts";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";

/** Search results retain timeline order. */
export function walkNavigation(walk: Walk) {
  const chaptersByStep = new Map(walk.chapters.flatMap((chapter) => chapter.stepKeys.map((key) => [key, chapter] as const)));
  const entries = walk.steps.map((step, index) => {
    const chapter = chaptersByStep.get(step.key);
    const operation = walkStepOperation(step);
    const title = step.kind === "effect" ? step.effect.path
      : step.kind === "command" ? commandWindowLine(step.command)
      : step.kind === "git" ? gitChangeLine(step.change) : walkGroupFileSummary(step);
    const paths = walkStepFiles(step).map((file) => file.path).join(" ");
    const detail = step.kind === "effect" && step.effect.from_path ? `From ${step.effect.from_path}`
      : step.kind === "effect" ? step.effect.tool_name : paths;
    const context = chapter?.prompt || chapter?.title || "Source activity";
    const change = step.kind === "effect" ? wholeFileOpChange(step.effect.op) : undefined;
    return { index, title, operation, detail, change,
      search: [title, operation, detail, context, chapter?.title].join(" ").toLocaleLowerCase() };
  });
  const chapters = walkChapterSegments(walk).map((segment) => {
    const chapter = chaptersByStep.get(segment.key);
    return { ...segment, prompt: chapter?.prompt || entries[segment.start]?.title || "Recorded changes" };
  });
  return { entries, chapters };
}

export function searchWalkNavigation(entries: ReturnType<typeof walkNavigation>["entries"], query: string) {
  const words = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  return words.length ? entries.filter((entry) => words.every((word) => entry.search.includes(word))) : entries;
}
