import { describe, expect, it } from "vitest";
import {
  contractCompatible,
  contractMajor,
  hostSharesDevice,
  incompatibleHost,
  isCallerPerson,
  noteHostInfo,
  resetHostIdentityForTest,
} from "./host-identity.ts";
import { TEST_OWNER_PERSON_ID, testHostInfo } from "./host-identity-test.ts";

const OTHER_HOST_ID = "00000000-0000-4000-8000-0000000000f1";

describe("host contract compatibility", () => {
  it("accepts any minor or patch of the client's major version", () => {
    expect(contractCompatible("1.0.0", "1.0.0")).toBe(true);
    expect(contractCompatible("1.7.3", "1.0.0")).toBe(true);
  });

  it("rejects a different major version in either direction", () => {
    expect(contractCompatible("2.0.0", "1.4.0")).toBe(false);
    expect(contractCompatible("0.9.0", "1.0.0")).toBe(false);
  });

  it("rejects a contract version it cannot parse", () => {
    expect(contractMajor("v1.0.0")).toBeNull();
    expect(contractMajor(" 1.0.0 ")).toBeNull();
    expect(contractMajor("=1.0.0")).toBeNull();
    expect(contractMajor("1.0.0-rc.1+build.7")).toBe(1);
    expect(contractCompatible("1.0", "1.0.0")).toBe(false);
    expect(contractCompatible("", "1.0.0")).toBe(false);
  });

  it("names an incompatible connected host", () => {
    resetHostIdentityForTest(testHostInfo({ contract_version: "2.1.0" }));
    expect(incompatibleHost()?.contract_version).toBe("2.1.0");
    resetHostIdentityForTest(testHostInfo());
    expect(incompatibleHost()).toBeNull();
  });
});

describe("host generation", () => {
  it("does not treat the first handshake as a host change", () => {
    resetHostIdentityForTest();
    expect(noteHostInfo(testHostInfo())).toEqual({ hostChanged: false, compatible: true });
  });

  it("does not treat the same install after a restart as a host change", () => {
    resetHostIdentityForTest();
    noteHostInfo(testHostInfo());
    expect(noteHostInfo(testHostInfo({ product_version: "9.9.9" })).hostChanged).toBe(false);
  });

  it("signals rehydration when a different install answers", () => {
    resetHostIdentityForTest();
    noteHostInfo(testHostInfo());
    expect(noteHostInfo(testHostInfo({ host_id: OTHER_HOST_ID })).hostChanged).toBe(true);
    expect(noteHostInfo(testHostInfo({ host_id: OTHER_HOST_ID })).hostChanged).toBe(false);
  });

  it("reports a major mismatch without hiding the host change", () => {
    resetHostIdentityForTest(testHostInfo());
    expect(noteHostInfo(testHostInfo({ host_id: OTHER_HOST_ID, contract_version: "3.0.0" })))
      .toEqual({ hostChanged: true, compatible: false });
  });
});

describe("caller and capabilities", () => {
  it("claims a person as the caller only by id", () => {
    resetHostIdentityForTest(testHostInfo());
    expect(isCallerPerson(TEST_OWNER_PERSON_ID)).toBe(true);
    expect(isCallerPerson("00000000-0000-4000-8000-0000000000b2")).toBe(false);
    expect(isCallerPerson("")).toBe(false);
  });

  it("claims no one before the handshake is read", () => {
    resetHostIdentityForTest();
    expect(isCallerPerson(TEST_OWNER_PERSON_ID)).toBe(false);
    expect(isCallerPerson("")).toBe(false);
    expect(isCallerPerson(undefined)).toBe(false);
  });

  it("offers host paths only with shared_device", () => {
    resetHostIdentityForTest(testHostInfo());
    expect(hostSharesDevice()).toBe(true);
    resetHostIdentityForTest(testHostInfo({ capabilities: [] }));
    expect(hostSharesDevice()).toBe(false);
    resetHostIdentityForTest();
    expect(hostSharesDevice()).toBe(false);
  });
});
