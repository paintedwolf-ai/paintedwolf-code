import type { LycaonClient } from "../../api/client.ts";
import { latestWalkTurnStart, walkChapterStart } from "./walk-chapters.ts";
import { loadWalk } from "./walk-loader.ts";
import { walkStepIndexForFile, type Walk, type WalkFileTarget, type WalkStep } from "./walk-model.ts";
import { loadSourceComparison } from "../source/source-comparison-cache.ts";
import { loadWalkGitReview } from "./walk-git-loading.ts";

type Work = { client: LycaonClient; projectId: string; key: string; kind: "entry" | "neighbor"; run: () => Promise<unknown> };
let pending: Work[] = [];
const running = new Set<Work>();
let generation = 0;

function drain(): void {
  while (running.size < 2 && pending.length) {
    const work = pending.shift();
    if (!work) break;
    const epoch = generation;
    running.add(work);
    void Promise.resolve().then(() => generation === epoch ? work.run() : undefined).catch(() => undefined).finally(() => {
      if (generation !== epoch) return;
      running.delete(work);
      drain();
    });
  }
}

function enqueue(work: Work): void {
  if ([...pending, ...running].some(held => held.client === work.client && held.projectId === work.projectId && held.key === work.key)) return;
  pending.push(work);
  pending = pending.slice(-8);
  drain();
}

async function prepareStep(client: LycaonClient, projectId: string, sessionId: string, step: WalkStep | undefined): Promise<void> {
  if (step?.kind === "effect") await loadSourceComparison(client, projectId, { effectId: step.effect.id }, sessionId);
  if (step?.kind === "git") await loadWalkGitReview(client, projectId, step.change.id, { sessionId });
}

/** Warms comparisons for adjacent steps without changing selection. */
export function prefetchWalkNeighbors(client: LycaonClient, projectId: string, sessionId: string, walk: Walk, at: number): void {
  pending = pending.filter(work => work.kind !== "neighbor" || work.client !== client || work.projectId !== projectId);
  for (const index of [at + 1, at - 1]) {
    const step = walk.steps[index];
    if (step && (step.kind === "effect" || step.kind === "git")) enqueue({
      client, projectId, kind: "neighbor", key: JSON.stringify([sessionId, step.key]),
      run: () => prepareStep(client, projectId, sessionId, step),
    });
  }
}

export function prepareWalk(client: LycaonClient, projectId: string, sessionId: string, target?: WalkFileTarget | null, messageId?: string): void {
  const epoch = generation;
  pending = pending.filter(work => work.kind !== "entry" || work.client !== client || work.projectId !== projectId);
  enqueue({ client, projectId, kind: "entry", key: JSON.stringify([sessionId, target, messageId]), run: async () => {
    const walk = await loadWalk(client, projectId, sessionId);
    if (generation !== epoch) return;
    const chapter = messageId ? walk.chapters.find(chapter => chapter.messageId === messageId) : undefined;
    if (messageId && !chapter) return;
    const file = messageId ? -1 : walkStepIndexForFile(walk, target);
    const at = file >= 0 ? file : chapter ? walkChapterStart(walk, chapter) : latestWalkTurnStart(walk);
    await prepareStep(client, projectId, sessionId, walk.steps[at]);
    if (generation === epoch) prefetchWalkNeighbors(client, projectId, sessionId, walk, at);
  } });
}

export function resetWalkPrefetchForTests(): void {
  generation++;
  pending = [];
  running.clear();
}
