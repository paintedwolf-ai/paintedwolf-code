// Ollama discovery for mock E2E runs: one tool-capable model, no inference.
// Writes the bound port to MODEL_FIXTURE_PORT_FILE once it is listening.
const portFile = process.env.MODEL_FIXTURE_PORT_FILE;
if (!portFile) {
  throw new Error("MODEL_FIXTURE_PORT_FILE is required");
}

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: Number(process.env.MODEL_FIXTURE_PORT ?? 0),
  fetch(request) {
    const path = new URL(request.url).pathname;
    if (request.method === "GET" && path === "/api/tags") {
      return Response.json({ models: [{ name: "mock-model" }] });
    }
    if (request.method === "POST" && path === "/api/show") {
      return Response.json({ capabilities: ["completion", "tools"] });
    }
    return new Response("not found", { status: 404 });
  },
});

await Bun.write(portFile, `${server.port}\n`);
