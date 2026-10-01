import { describe, expect, it } from "vitest";
import { managedSecretId } from "./managed-secret-reference.ts";

const ID = "123e4567-e89b-12d3-a456-426614174000";
const REFERENCE = `{{paintedwolf-secret:${ID}}}`;

describe("managedSecretId", () => {
  it.each([REFERENCE, `  ${REFERENCE}\n`])("parses a complete reference: %s", (value) => {
    expect(managedSecretId(value)).toBe(ID);
  });

  it.each([
    "not a reference",
    "{{paintedwolf-secret:abc}}",
    "{{paintedwolf-secret:------------------------------------}}",
    "{{paintedwolf-secret:123e4567e-89b-12d3-a456-426614174000}}",
    `{{secret:${ID}}}`,
    REFERENCE.replace("123e", "123E"),
    `prefix ${REFERENCE}`,
    `${REFERENCE} suffix`,
    `${REFERENCE}${REFERENCE}`,
  ])("rejects malformed or embedded references: %s", (value) => {
    expect(managedSecretId(value)).toBeNull();
  });
});
