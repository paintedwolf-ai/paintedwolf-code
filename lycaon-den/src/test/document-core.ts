import { spawn } from "node:child_process";
import { afterAll } from "vitest";

type CoreRequest = {
  action: string;
  handle: number;
  client?: number;
  update?: string;
  vector?: string;
  edits?: { index: number; delete: number; insert: string }[];
  checkpoint?: boolean;
};

type CoreResponse = {
  text: string;
  vector: string;
  update: string;
  checkpoint: string;
  error?: string;
};

const MEMORY_LIMIT_BYTES = 256 << 20;
const running = new Set<() => void>();
afterAll(() => {
  for (const stop of running) stop();
});

/** Runs the host's native document core, the executable the engine ships. */
export async function documentCore(): Promise<(request: CoreRequest) => Promise<CoreResponse>> {
  const binary = process.env.LYCAON_DOCUMENT_CORE_BINARY;
  if (!binary) throw new Error("LYCAON_DOCUMENT_CORE_BINARY names no document core; run tests through ./task");
  const child = spawn(binary, ["serve", "--memory-limit-bytes", String(MEMORY_LIMIT_BYTES)], {
    stdio: ["pipe", "pipe", "inherit"],
  });
  const stop = () => child.stdin.end();
  running.add(stop);

  const waiting: { resolve: (frame: Buffer) => void; reject: (error: Error) => void }[] = [];
  let exited: Error | undefined;
  child.once("exit", (code, signal) => {
    running.delete(stop);
    exited = new Error(`document core exited (${signal ?? code})`);
    for (const pending of waiting.splice(0)) pending.reject(exited);
  });
  let buffered = Buffer.alloc(0);
  child.stdout.on("data", (chunk: Buffer) => {
    buffered = Buffer.concat([buffered, chunk]);
    while (buffered.length >= 4 && buffered.length >= 4 + buffered.readUInt32LE(0)) {
      const length = buffered.readUInt32LE(0);
      const frame = buffered.subarray(4, 4 + length);
      buffered = buffered.subarray(4 + length);
      waiting.shift()?.resolve(frame);
    }
  });
  const next = () => new Promise<Buffer>((resolve, reject) => {
    if (exited) reject(exited);
    else waiting.push({ resolve, reject });
  });

  const hello = JSON.parse((await next()).toString("utf8")) as { protocol: number };
  if (hello.protocol !== 1) throw new Error(`document core speaks protocol ${hello.protocol}`);
  let queue = Promise.resolve();
  return (request) => {
    const answer = queue.then(async () => {
      const payload = Buffer.from(JSON.stringify(request), "utf8");
      const header = Buffer.alloc(4);
      header.writeUInt32LE(payload.length, 0);
      const response = next();
      child.stdin.write(Buffer.concat([header, payload]));
      return JSON.parse((await response).toString("utf8")) as CoreResponse;
    });
    queue = answer.then(() => undefined, () => undefined);
    return answer;
  };
}
