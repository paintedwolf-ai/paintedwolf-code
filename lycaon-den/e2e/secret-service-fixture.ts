import { randomBytes } from "node:crypto";
import { createServer } from "node:http";

export async function startSecretService() {
  const password = randomBytes(24).toString("hex");
  let configured = false;
  let authenticatedRequests = 0;
  const server = createServer(async (request, response) => {
    if (request.method === "POST" && request.url === "/configure") {
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(Buffer.from(chunk));
      configured = Buffer.concat(chunks).toString() === password;
      response.writeHead(configured ? 204 : 403).end();
      return;
    }
    const authorized = configured && request.headers.authorization ===
      `Basic ${Buffer.from(`test:${password}`).toString("base64")}`;
    if (authorized) authenticatedRequests++;
    response.writeHead(authorized ? 200 : 403, { "Content-Type": "text/plain" });
    response.end(authorized ? "authenticated" : "denied");
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Missing service port");
  return {
    password,
    port: address.port,
    origin: `http://127.0.0.1:${address.port}`,
    configured: () => configured,
    authenticatedRequests: () => authenticatedRequests,
    close: () => new Promise<void>((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve());
      server.closeAllConnections();
    }),
  };
}
