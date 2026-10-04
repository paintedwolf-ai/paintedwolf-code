import { createServer } from "node:http";

/** A local service whose password is whatever its first configuration sets. */
export async function startSecretService() {
  let password = "";
  let authenticatedRequests = 0;
  const server = createServer(async (request, response) => {
    if (request.method === "POST" && request.url === "/configure") {
      const chunks: Buffer[] = [];
      for await (const chunk of request) chunks.push(Buffer.from(chunk));
      const offered = Buffer.concat(chunks).toString();
      if (!password && offered) password = offered;
      response.writeHead(password && offered === password ? 204 : 403).end();
      return;
    }
    const authorized = password !== "" && request.headers.authorization ===
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
    port: address.port,
    origin: `http://127.0.0.1:${address.port}`,
    password: () => password,
    configured: () => password !== "",
    authenticatedRequests: () => authenticatedRequests,
    close: () => new Promise<void>((resolve, reject) => {
      server.close((error) => error ? reject(error) : resolve());
      server.closeAllConnections();
    }),
  };
}
