import type { FileBufferKey } from "../components/project-files-model.ts";
import type { FileBuffer } from "./files-buffer-state.ts";

/** Tracks the newest operation across changing targets. */
export class LatestOperation {
  #generation = 0;
  #current = 0;

  begin(): number {
    const generation = ++this.#generation;
    this.#current = generation;
    return generation;
  }

  isCurrent(generation: number): boolean {
    return this.#current === generation;
  }

  finish(generation: number): void {
    if (this.isCurrent(generation)) this.#current = 0;
  }

  invalidate(): void {
    this.#current = 0;
  }
}

/** Coalesces loads within one opening; reopening replaces its pending work. */
export class FilesBufferExclusiveRequests {
  readonly #activeTokens = new Map<FileBufferKey, { token: number; opening: object }>();
  #nextToken = 0;

  begin(key: FileBufferKey, opening: object): number | null {
    if (this.#activeTokens.get(key)?.opening === opening) return null;
    const token = ++this.#nextToken;
    this.#activeTokens.set(key, { token, opening });
    return token;
  }

  isCurrent(key: FileBufferKey, token: number): boolean {
    return this.#activeTokens.get(key)?.token === token;
  }

  inFlight(key: FileBufferKey): boolean {
    return this.#activeTokens.has(key);
  }

  finish(key: FileBufferKey, token: number): void {
    if (this.isCurrent(key, token)) this.#activeTokens.delete(key);
  }

  clear(): void {
    this.#activeTokens.clear();
  }
}

/** Load admission shared by every mounted Files stage. */
export const sharedBufferLoadRequests = new FilesBufferExclusiveRequests();

/** Versions refreshes so only the latest request for each buffer may settle. */
export class FilesBufferLatestRequests {
  readonly #generations = new Map<FileBufferKey, number>();
  #nextGeneration = 0;

  begin(key: FileBufferKey): number {
    const generation = ++this.#nextGeneration;
    this.#generations.set(key, generation);
    return generation;
  }

  isCurrent(key: FileBufferKey, generation: number): boolean {
    return this.#generations.get(key) === generation;
  }

  finish(key: FileBufferKey, generation: number): void {
    if (this.isCurrent(key, generation)) this.#generations.delete(key);
  }
}

export function bufferRequestIdentity(
  projectId: string,
  buffer: Pick<FileBuffer, "rootId" | "path" | "jobId">,
): string {
  return [
    projectId,
    buffer.rootId,
    buffer.path,
    buffer.jobId ?? "",
  ].join("\u0000");
}

