import { describe, expect, it } from "vitest";
import { API_OPERATIONS } from "./operations.generated.ts";
import { assertOperationQuery, assertOperationRequest } from "./operation-contract.ts";
import { OperationContractError } from "./operation-contract-error.ts";

type QueryParameter = NonNullable<ReturnType<typeof assertOperationRequest>["query"]>[number];

const cases: { id: string; template: string; path: string; method: string; parameters: readonly QueryParameter[] }[] = Object.entries(API_OPERATIONS).map(([id, operation]) => ({
  id,
  template: operation.path,
  path: operation.path.replaceAll(/\{[^{}]+\}/g, "fixture%2Fsegment"),
  method: operation.method,
  parameters: "query" in operation ? operation.query : [],
}));

function sample(parameter: QueryParameter): string {
  if (parameter.enum?.length) return String(parameter.enum[0]);
  if (parameter.type === "boolean") return "true";
  if (parameter.type === "string") return "x".repeat(parameter.minLength ?? 1);
  return String(parameter.minimum ?? parameter.maximum ?? 0);
}

function withValues(params: URLSearchParams, parameter: QueryParameter, value: string, count = parameter.minItems ?? 1): URLSearchParams {
  const next = new URLSearchParams(params);
  next.delete(parameter.name);
  for (let i = 0; i < count; i += 1) next.append(parameter.name, value);
  return next;
}

describe("generated operation query contract", () => {
  it.each(cases)("$id rejects undeclared keys and requires declared keys", ({ path, template, method, parameters }) => {
    const required = parameters.filter((parameter) => parameter.required)
      .reduce((params, parameter) => withValues(params, parameter, sample(parameter)), new URLSearchParams());
    expect(assertOperationRequest(path, method.toLowerCase()).path).toBe(template);
    expect(() => assertOperationQuery(`${path}?${required}`, method)).not.toThrow();
    const extra = new URLSearchParams(required);
    for (const key of ["__proto__", "constructor", "undeclared_fixture_key"]) {
      extra.set(key, "x");
      expect(() => assertOperationQuery(`${path}?${extra}`, method)).toThrow(OperationContractError);
      extra.delete(key);
    }
    for (const parameter of parameters.filter((candidate) => candidate.required)) {
      const missing = withValues(required, parameter, "", 0);
      expect(() => assertOperationQuery(missing.size ? `${path}?${missing}` : path, method)).toThrow(OperationContractError);
    }
  });

  it.each(cases.filter(({ parameters }) => parameters.length))("$id enforces type, bounds, and repetition", ({ path, method, parameters }) => {
    const required = parameters.filter((parameter) => parameter.required)
      .reduce((params, parameter) => withValues(params, parameter, sample(parameter)), new URLSearchParams());
    for (const parameter of parameters) {
      const { minimum, maximum, minLength, maxLength, minItems, maxItems } = parameter;
      const validate = (value: string, count?: number) => assertOperationQuery(
        `${path}?${withValues(required, parameter, value, count)}`,
        method,
      );
      const valid = (value: string, count?: number) => expect(() => validate(value, count), parameter.name).not.toThrow();
      const invalid = (value: string, count?: number) => expect(() => validate(value, count), parameter.name).toThrow(OperationContractError);

      const example = sample(parameter);
      valid(example);
      if (minimum !== undefined) {
        const bound = parameter.type === "integer" ? Math.ceil(minimum) : minimum;
        if (!parameter.enum || parameter.enum.includes(bound)) valid(String(bound));
        invalid(String(bound - 1));
      }
      if (maximum !== undefined) {
        const bound = parameter.type === "integer" ? Math.floor(maximum) : maximum;
        if (!parameter.enum || parameter.enum.includes(bound)) valid(String(bound));
        invalid(String(bound + 1));
      }
      if (minLength !== undefined) {
        const atBound = parameter.enum?.find((value) => Array.from(String(value)).length === minLength);
        if (!parameter.enum || atBound !== undefined) valid(String(atBound ?? "🐺".repeat(minLength)));
        if (minLength > 0) invalid("🐺".repeat(minLength - 1));
      }
      if (maxLength !== undefined) {
        const atBound = parameter.enum?.find((value) => Array.from(String(value)).length === maxLength);
        if (!parameter.enum || atBound !== undefined) valid(String(atBound ?? "🐺".repeat(maxLength)));
        invalid("🐺".repeat(maxLength + 1));
      }
      if (parameter.type === "integer" || parameter.type === "number") {
        for (const value of ["0x10", " 1", "NaN", "Infinity", ""]) invalid(value);
      }
      if (parameter.type === "integer") invalid("1.5");
      if (parameter.type === "boolean") {
        if (!parameter.enum || parameter.enum.includes(true)) valid("true");
        if (!parameter.enum || parameter.enum.includes(false)) valid("false");
        for (const value of ["True", "yes", "0", ""]) invalid(value);
      }
      if (parameter.enum) {
        for (const value of parameter.enum) valid(String(value));
        invalid(parameter.type === "string" ? "outside_fixture_enum" : "99999999");
      }
      if (parameter.repeatable) {
        if (maxItems === undefined || maxItems > 1) valid(example, Math.max(2, minItems ?? 1));
        if (minItems !== undefined && minItems > 1) invalid(example, minItems - 1);
        if (maxItems !== undefined) {
          valid(example, maxItems);
          invalid(example, maxItems + 1);
        }
      } else {
        invalid(example, 2);
      }
    }
  });
});
