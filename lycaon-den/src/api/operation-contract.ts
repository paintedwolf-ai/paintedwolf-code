import { API_OPERATIONS } from "./operations.generated.ts";

export type QueryParameter = {
  name: string;
  type: "integer" | "number" | "string" | "boolean";
  required?: boolean;
  repeatable?: boolean;
  minimum?: number;
  maximum?: number;
  enum?: readonly (string | number | boolean)[];
  minLength?: number;
  maxLength?: number;
  minItems?: number;
  maxItems?: number;
};

type Operation = {
  method: string;
  path: string;
  query?: readonly QueryParameter[];
};

export class OperationContractError extends Error {
  override name = "OperationContractError";
}

export function operationPattern(template: string): RegExp {
  const escaped = template.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`^${escaped.replace(/\\\{[^{}]+\\\}/g, "[^/]+")}$`);
}

type TemplatedOperation = { operation: Operation; pattern: RegExp; specificity: string };
const exactOperations = new Map<string, Operation>();
const templatedOperations = new Map<string, TemplatedOperation[]>();
for (const operation of Object.values(API_OPERATIONS)) {
  if (!operation.path.includes("{")) {
    exactOperations.set(`${operation.method} ${operation.path}`, operation);
    continue;
  }
  const templates = templatedOperations.get(operation.method) ?? [];
  const specificity = operation.path.split("/").map((segment) => segment.includes("{") ? "0" : "1").join("");
  templates.push({ operation, pattern: operationPattern(operation.path), specificity });
  templatedOperations.set(operation.method, templates);
}

// At the first differing segment, a literal route takes precedence.
for (const templates of templatedOperations.values()) {
  templates.sort((a, b) => b.specificity.localeCompare(a.specificity));
}

let observer: ((error: OperationContractError) => void) | undefined;

/** Captures a contract failure before the request promise rejects. */
export function observeOperationContract(run: () => void): OperationContractError | undefined {
  const previous = observer;
  let first: OperationContractError | undefined;
  observer = (error) => { first ??= error; };
  try {
    run();
    return first;
  } finally {
    observer = previous;
  }
}

function fail(message: string): never {
  const error = new OperationContractError(message);
  observer?.(error);
  throw error;
}

export function assertOperationRequest(path: string, method: string): Operation {
  const pathname = path.split("?", 1)[0] ?? path;
  const normalizedMethod = method.toUpperCase();
  const operation = exactOperations.get(`${normalizedMethod} ${pathname}`)
    ?? templatedOperations.get(normalizedMethod)?.find(({ pattern }) => pattern.test(pathname))?.operation;
  return operation ?? fail(`Unknown API operation ${normalizedMethod} ${pathname}`);
}

export function assertOperationQuery(path: string, method: string): void {
  const operation = assertOperationRequest(path, method);
  const queryStart = path.indexOf("?");
  const params = new URLSearchParams(queryStart < 0 ? "" : path.slice(queryStart + 1));
  const declared = new Map(operation.query?.map((parameter) => [parameter.name, parameter]));
  for (const key of new Set(params.keys())) {
    const parameter = declared.get(key);
    if (!parameter) queryError(operation, key, "is not declared");
    const values = params.getAll(key);
    const countError = queryCountError(parameter, values.length);
    if (countError) queryError(operation, key, countError);
    for (const value of values) {
      const valueError = queryValueError(parameter, value);
      if (valueError) queryError(operation, key, valueError);
    }
    declared.delete(key);
  }
  for (const parameter of declared.values()) {
    if (parameter.required) queryError(operation, parameter.name, "is required");
  }
}

function queryError(operation: Operation, name: string, reason: string): never {
  return fail(`Query parameter "${name}" ${reason} on ${operation.method} ${operation.path}`);
}

function queryCountError(parameter: QueryParameter, count: number): string | undefined {
  if (count > 1 && !parameter.repeatable) return "is repeated";
  if (parameter.minItems !== undefined && count < parameter.minItems) return `requires at least ${parameter.minItems} values`;
  if (parameter.maxItems !== undefined && count > parameter.maxItems) return `allows at most ${parameter.maxItems} values`;
}

const integerPattern = /^[+-]?\d+$/;
const numberPattern = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/;

function queryValueError(parameter: QueryParameter, value: string): string | undefined {
  let decoded: string | number | boolean = value;
  switch (parameter.type) {
    case "integer":
    case "number": {
      const integer = parameter.type === "integer";
      const number = Number(value);
      if (integer ? !integerPattern.test(value) || !Number.isSafeInteger(number)
        : !numberPattern.test(value) || !Number.isFinite(number)) return `must be of type ${parameter.type}`;
      if (parameter.minimum !== undefined && number < parameter.minimum) return `is below minimum ${parameter.minimum}`;
      if (parameter.maximum !== undefined && number > parameter.maximum) return `exceeds maximum ${parameter.maximum}`;
      decoded = number;
      break;
    }
    case "boolean":
      if (value !== "true" && value !== "false") return "is not a boolean";
      decoded = value === "true";
      break;
    case "string": {
      const length = [...value].length;
      if (parameter.minLength !== undefined && length < parameter.minLength) return `is shorter than ${parameter.minLength} characters`;
      if (parameter.maxLength !== undefined && length > parameter.maxLength) return `is longer than ${parameter.maxLength} characters`;
      break;
    }
  }
  if (parameter.enum && !parameter.enum.includes(decoded)) return "is outside its enum";
}
