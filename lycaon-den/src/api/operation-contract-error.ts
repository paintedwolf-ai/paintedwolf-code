export class OperationContractError extends Error {
  override name = "OperationContractError";

  static #observer: ((error: OperationContractError) => void) | undefined;

  static raise(message: string): never {
    const error = new OperationContractError(message);
    OperationContractError.#observer?.(error);
    throw error;
  }

  /** Captures a contract failure before the request promise rejects. */
  static capture(run: () => void): OperationContractError | undefined {
    const previous = OperationContractError.#observer;
    let first: OperationContractError | undefined;
    OperationContractError.#observer = (error) => { first ??= error; };
    try {
      run();
      return first;
    } finally {
      OperationContractError.#observer = previous;
    }
  }
}
