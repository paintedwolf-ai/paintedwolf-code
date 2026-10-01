const ROLE_ASSISTANT = "assistant";
const CH_ANALYSIS = "analysis";
const CH_COMMENTARY = "commentary";
const CH_FINAL = "final";

function startsChannel(s: string): boolean {
  return (
    s.startsWith(CH_ANALYSIS) ||
    s.startsWith(CH_COMMENTARY) ||
    s.startsWith(CH_FINAL)
  );
}

function peelRolePrefix(s: string): string {
  while (s.startsWith(ROLE_ASSISTANT)) {
    const rest = s.slice(ROLE_ASSISTANT.length);
    if (
      rest.startsWith(ROLE_ASSISTANT) ||
      startsChannel(rest) ||
      rest.startsWith("{")
    ) {
      s = rest;
      continue;
    }
    break;
  }
  return s;
}

/** Strip a leading role/final prefix when the rest starts a JSON object. */
export function peelFinalEnvelope(content: string): string {
  const s = content.trim();
  if (!s) return s;
  const peeled = peelRolePrefix(s);
  if (peeled.startsWith(CH_FINAL)) {
    const rest = peeled.slice(CH_FINAL.length).trim();
    if (rest.startsWith("{")) return rest;
  }
  if (peeled.startsWith("{") && peeled !== s) return peeled;
  return s;
}
