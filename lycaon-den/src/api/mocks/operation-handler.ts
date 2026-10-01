import { http, HttpResponse, type DefaultBodyType, type HttpResponseResolver, type PathParams } from "msw";
import { API_OPERATIONS, type OperationId, type OperationResponse } from "../operations.generated.ts";

export const MSW_API_BASE = "http://127.0.0.1:8787";

type Answer<Id extends OperationId> = OperationResponse<Id> | Response;

type Resolver<Id extends OperationId> = (
  info: Parameters<HttpResponseResolver<PathParams, DefaultBodyType>>[0],
) => Answer<Id> | Promise<Answer<Id>>;

/**
 * An MSW handler for one OpenAPI operation: its method and route come from the
 * generated operation table and its body is checked against the operation's
 * success response.
 */
export function operationHandler<Id extends OperationId>(id: Id, resolve: Resolver<Id>, base = MSW_API_BASE) {
  const operation = API_OPERATIONS[id];
  const route = `${base}${operation.path.replace(/\{([^{}]+)\}/g, ":$1")}`;
  const method = operation.method.toLowerCase() as "get" | "post" | "put" | "patch" | "delete";
  return http[method](route, async (info) => {
    const answer = await resolve(info);
    return answer instanceof Response ? answer : HttpResponse.json(answer as DefaultBodyType);
  });
}
