import { render } from "@solidjs/testing-library";
import type { JSX } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { auditRender, clearMarkedSecrets, markSecret } from "./render-audit.ts";

function checks(ui: () => JSX.Element): string[] {
  const view = render(ui);
  return auditRender(view.container).map((finding) => finding.check);
}

describe("auditRender", () => {
  afterEach(clearMarkedSecrets);

  it("accepts controls named by text, attribute, label or reference", () => {
    expect(checks(() => (
      <div>
        <button>Text</button>
        <button aria-label="Attribute" />
        <button title="Title" />
        <span id="reference">Reference</span>
        <button aria-labelledby="reference" />
        <label>Wrapped <input /></label>
        <label for="target">For</label><input id="target" />
        <input placeholder="Placeholder" />
        <input type="submit" value="Send" />
        <button><img src="x.png" alt="Icon" /></button>
      </div>
    ))).toEqual([]);
  });

  it("reports an unnamed control unless it is hidden", () => {
    expect(checks(() => <button />)).toEqual(["unnamed_control"]);
    expect(checks(() => <div hidden><button /></div>)).toEqual([]);
    expect(checks(() => <button aria-hidden="true" />)).toEqual([]);
  });

  it.each([
    ["an undefined value", "Size undefined"],
    ["a failed number", "Total NaN"],
    ["a stringified object", "Owner [object Object]"],
    ["an unrendered template", "Hello {{ name }}"],
  ])("reports %s in text", (_name, text) => {
    expect(checks(() => <p>{text}</p>)).toEqual(["broken_text"]);
  });

  it("reads naming attributes as well as text", () => {
    expect(checks(() => <button aria-label="Open undefined" />)).toEqual(["broken_text"]);
  });

  it("does not match words that merely contain a checked term", () => {
    expect(checks(() => <p>undefinedness and Nancy and lycaonidae</p>)).toEqual([]);
  });

  it("reports the engine name in visible text", () => {
    expect(checks(() => <p>Restart Lycaon</p>)).toEqual(["engine_name"]);
  });

  it("reports a marked secret and forgets it when cleared", () => {
    const secret = markSecret("fixture-secret-value");
    expect(checks(() => <p>{secret}</p>)).toEqual(["secret_exposed"]);
    clearMarkedSecrets();
    expect(checks(() => <p>{secret}</p>)).toEqual([]);
  });
});
