/** The handshake a loopback host gives its local caller. */
import { API_CONTRACT_VERSION } from "../../api/operations.generated.ts";
import type { HostInfo } from "../../api/types.ts";

export const TEST_OWNER_PERSON_ID = "00000000-0000-4000-8000-000000000002";

export const TEST_HOST_INFO: HostInfo = {
  host_id: "00000000-0000-4000-8000-0000000000a1",
  host_public_key: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  product_version: "0.0.0",
  contract_version: API_CONTRACT_VERSION,
  caller: { id: TEST_OWNER_PERSON_ID, role: "owner" },
  capabilities: ["shared_device"],
};

export function testHostInfo(overrides: Partial<HostInfo> = {}): HostInfo {
  return { ...TEST_HOST_INFO, ...overrides };
}
