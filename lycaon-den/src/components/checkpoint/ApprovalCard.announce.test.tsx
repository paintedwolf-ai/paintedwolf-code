import type { ComponentProps } from "solid-js";
import { render } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";

const announce = vi.hoisted(() => vi.fn());
vi.mock("../../platform/interaction/announcer.tsx", () => ({ announce }));
vi.mock("./ToolApprovalBody.tsx", () => ({ ToolApprovalBody: () => <div /> }));
vi.mock("./ContentApplyBody.tsx", () => ({ ContentApplyBody: () => <div /> }));

import { ApprovalCard } from "./ApprovalCard.tsx";
import { checkpointRowLabel } from "./checkpoint-row-label.ts";
import { checkpoint } from "./approval-card-test-fixtures.ts";

describe("ApprovalCard arrival", () => {
  it("announces the pending approval once when the card appears", () => {
    const pending = checkpoint();
    const props = { checkpoint: pending } as unknown as ComponentProps<typeof ApprovalCard>;
    render(() => <ApprovalCard {...props} />);
    expect(announce).toHaveBeenCalledTimes(1);
    expect(announce).toHaveBeenCalledWith(`Needs approval: ${checkpointRowLabel(pending)}`);
  });
});
