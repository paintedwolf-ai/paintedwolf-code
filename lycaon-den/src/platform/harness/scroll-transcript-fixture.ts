import type { Message } from "../../api/types.ts";

/** Varied persisted rows for native input and virtualization qualification. */
export function scrollTranscriptFixture(turns: number, start = 0, activity = false): Message[] {
  if (!Number.isInteger(turns) || turns < 1 || turns > 200 || !Number.isInteger(start) || start < 0) {
    throw new Error("scroll transcript requires 1 to 200 turns and a nonnegative start");
  }
  const message = (role: Message["role"], content: string): Message => ({
    id: crypto.randomUUID(), role, content, created_at: new Date().toISOString(),
    origin: role === "user" ? "user" : role === "tool" ? "tool" : "model",
    authority: role === "user" ? "user" : "none",
    trust_tier: role === "tool" ? "untrusted" : "trusted",
  });
  const shapes = [
    (i: number) => `Step ${i} walks through the setup.\n\n` + "The profile holds the team and the entitlements it grants. ".repeat(12),
    (i: number) => `Checklist ${i}:\n\n` + Array.from({ length: 8 }, (_, n) => `- Item ${n + 1}: confirm the signing identity and provisioning profile match.`).join("\n"),
    (i: number) => `Run this for part ${i}:\n\n\`\`\`sh\n` + Array.from({ length: 10 }, (_, n) => `security cms -D -i profile-${n}.provisionprofile | plutil -p -`).join("\n") + "\n```\n\nThen verify the output.",
  ];
  const messages = Array.from({ length: turns }, (_, i) => [
    message("user", `Part ${start + i + 1}: walk me through the next step.`),
    message("assistant", shapes[(start + i) % shapes.length]!(start + i + 1)),
  ]).flat();
  if (activity) {
    messages.push(message("user", "Can we improve the interface?"));
    const callRow = message("assistant", "");
    callRow.tool_calls = ["README.md", "main.go", "go.mod"].map(path => ({
      id: crypto.randomUUID(), name: "read", args: { path },
    }));
    messages.push(callRow);
    for (const call of callRow.tool_calls) {
      const content = `Contents of ${call.args?.path}:\n` + "The layout uses a spacing scale and an icon set.\n".repeat(12);
      messages.push({ ...message("tool", content), tool_result: {
        tool: call.name, tool_call_id: call.id, assistant_message_id: callRow.id,
        content, outcome: "completed",
      } });
    }
    messages.push(message("assistant", "Done."));
  }
  return messages;
}
