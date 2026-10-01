/** Claim-scoped value for overlapping component lifetimes. */
export type Claimable<T> = {
  get(): T | null;
  /** Assigns the value to one identity token. */
  claim(value: T, claimToken: object): void;
  /** Clears the value when the token still holds the claim. */
  release(claimToken: object): void;
};

export function createClaimable<T>(): Claimable<T> {
  let value: T | null = null;
  let holder: object | null = null;
  return {
    get: () => value,
    claim(next, claimToken) {
      value = next;
      holder = claimToken;
    },
    release(claimToken) {
      if (holder !== claimToken) return;
      value = null;
      holder = null;
    },
  };
}
