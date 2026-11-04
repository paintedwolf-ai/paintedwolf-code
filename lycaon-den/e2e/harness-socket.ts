import { mkdirSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { dirname } from "node:path";

/** Socket approval cards retain the actual service on Linux, so the path must be listening. */
export async function listenHarnessSocket(path: string): Promise<() => Promise<void>> {
  mkdirSync(dirname(path), { recursive: true });
  rmSync(path, { force: true });
  const server = createServer();
  server.unref();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(path, resolve);
  });
  return () => new Promise<void>((resolve) => server.close(() => resolve()));
}
