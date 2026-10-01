import { isRecord } from "../../../utils/type-guards.ts";
import { type ToolFact } from "../tool-presentation-contract.ts";
import { argRedactionField } from "../../transcript/content/redaction-spans.ts";
import { PATH_FACT_KEYS, sourceFactPath, capitalize, humanizeKey, formatScalar } from "./format.ts";

export function commandIOArgsFacts(
  args?: Record<string, unknown>,
  fieldPrefix?: string,
): ToolFact[] {
  if (!args) return [];
  const facts: ToolFact[] = [];
  const addPath = (label: string, key: string): void => {
    const value = args[key];
    if (typeof value !== "string" || !value.trim()) return;
    const rendered = value.trim();
    const fact: ToolFact = { label, value: rendered };
    const field = argFieldFor(fieldPrefix, key, value, rendered);
    if (field) fact.redactionField = field;
    facts.push(fact);
  };
  if (typeof args.stdin === "string" && args.stdin.trim()) {
    // Hidden stdin literals carry no redaction field.
    facts.push({ label: "Stdin", value: "literal provided" });
  }
  addPath("Stdin from", "stdin_from");
  if (isRecord(args.env) && Object.keys(args.env).length > 0) {
    facts.push({
      label: "Env",
      value: Object.keys(args.env).sort().join(", "),
    });
  }
  addPath("Stdout to", "stdout_to");
  addPath("Stderr to", "stderr_to");
  if (args.append === true) {
    facts.push({ label: "Append", value: "Yes" });
  }
  return facts;
}

export const COMMAND_IO_ARG_KEYS = new Set([
  "stdin",
  "stdin_from",
  "env",
  "stdout_to",
  "stderr_to",
  "append",
  "background",
]);

/** Returns the host field path for an unchanged rendered argument. */

function argFieldFor(
  fieldPrefix: string | undefined,
  key: string,
  value: unknown,
  rendered: string,
): string | undefined {
  if (!fieldPrefix || typeof value !== "string" || rendered !== value) {
    return undefined;
  }
  return argRedactionField(fieldPrefix, key);
}

export function factsFromArgs(
  args?: Record<string, unknown>,
  skipExtra?: ReadonlySet<string>,
  fieldPrefix?: string,
): ToolFact[] {
  if (!args) return [];
  const skip = new Set(["session_id", "project_id", "workspace_path"]);
  if (skipExtra) {
    for (const key of skipExtra) skip.add(key);
  }
  const facts: ToolFact[] = [];
  for (const [key, value] of Object.entries(args)) {
    if (skip.has(key) || value === undefined) continue;
    const text = formatScalar(value);
    if (text === "—") continue;
    const wide =
      key === "description" ||
      key === "prompt" ||
      key === "command" ||
      text.length > 80;
    const fact: ToolFact = { label: humanizeKey(key), value: text, wide };
    const field = argFieldFor(fieldPrefix, key, value, text);
    if (field) fact.redactionField = field;
    if (
      PATH_FACT_KEYS.has(key) &&
      typeof value === "string" &&
      value.trim() &&
      !value.includes("\n")
    ) {
      fact.path = sourceFactPath(value, args);
    }
    facts.push(fact);
  }
  return facts;
}

export function httpRequestArgsFacts(
  args?: Record<string, unknown>,
  fieldPrefix?: string,
): ToolFact[] {
  if (!args) return [];
  const facts: ToolFact[] = [];
  const add = (label: string, value: string, wide = false, key?: string): void => {
    const trimmed = value.trim();
    if (!trimmed) return;
    const fact: ToolFact = { label, value: trimmed, wide };
    const field = key
      ? argFieldFor(fieldPrefix, key, args[key], trimmed)
      : undefined;
    if (field) fact.redactionField = field;
    facts.push(fact);
  };

  if (typeof args.url === "string") add("URL", args.url, true, "url");
  add(
    "Method",
    typeof args.method === "string" ? args.method.toUpperCase() : "GET",
  );

  if (Array.isArray(args.query) && args.query.length > 0) {
    const names = args.query
      .map((param) =>
        isRecord(param) && typeof param.name === "string"
          ? param.name.trim()
          : "",
      )
      .filter(Boolean);
    add(
      "Query",
      names.length > 0 ? names.join(", ") : `${args.query.length} supplied`,
    );
  }

  // Basic-auth usernames can contain API keys; only the scheme is displayed.
  if (isRecord(args.auth) && typeof args.auth.scheme === "string") {
    add("Auth", capitalize(args.auth.scheme.trim().toLowerCase()));
  }

  if (Array.isArray(args.headers) && args.headers.length > 0) {
    const names = args.headers
      .map((header) =>
        isRecord(header) && typeof header.name === "string"
          ? header.name.trim()
          : "",
      )
      .filter(Boolean);
    add(
      "Headers",
      names.length > 0 ? names.join(", ") : `${args.headers.length} supplied`,
    );
  }

  if (typeof args.body_path === "string" && args.body_path.trim()) {
    facts.push({
      label: "Body file",
      value: args.body_path.trim(),
      path: { path: args.body_path.trim() },
    });
  } else if (Object.prototype.hasOwnProperty.call(args, "body_json")) {
    add("Body", "JSON value supplied");
  } else if (typeof args.body_text === "string") {
    add("Body", `Text supplied · ${args.body_text.length} characters`);
  } else if (Array.isArray(args.form) && args.form.length > 0) {
    const fields: string[] = [];
    for (const part of args.form) {
      if (!isRecord(part) || typeof part.name !== "string") continue;
      const name = part.name.trim();
      if (typeof part.path === "string" && part.path.trim()) {
        facts.push({
          label: `Form file · ${name}`,
          value: part.path.trim(),
          path: { path: part.path.trim() },
        });
      } else if (name) {
        fields.push(name);
      }
    }
    if (fields.length > 0) add("Form fields", fields.join(", "));
  }

  if (typeof args.cookie_jar === "string" && args.cookie_jar.trim()) {
    add("Cookie jar", args.cookie_jar.trim());
  }
  if (typeof args.timeout_ms === "number") {
    add("Timeout", `${args.timeout_ms} ms`);
  }
  if (typeof args.redirects === "string") add("Redirects", args.redirects);
  if (typeof args.response_path === "string" && args.response_path.trim()) {
    facts.push({
      label: "Response file",
      value: args.response_path.trim(),
      path: { path: args.response_path.trim() },
    });
  } else if (typeof args.response_body === "string") {
    add("Response body", args.response_body);
  }

  if (isRecord(args.capability_request)) {
    const loopback = args.capability_request.loopback_connect;
    if (isRecord(loopback) && Array.isArray(loopback.ports)) {
      add("Loopback ports", loopback.ports.map(formatScalar).join(", "));
    }
  }
  if (typeof args.unredact === "string" && args.unredact.trim()) {
    add("Redaction receipt", "Provided");
  }
  return facts;
}
