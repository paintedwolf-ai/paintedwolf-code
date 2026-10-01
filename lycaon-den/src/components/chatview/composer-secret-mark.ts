import { managedSecretId } from "../../api/managed-secret-reference.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { CreateComposerSecretRequest, ManagedSecret } from "../../api/types.ts";
import { MarkSecretFailure } from "../source/secrets/mark-secret-failure.ts";
import { SECRET_SPAN_COPY } from "../source/secrets/secret-span-copy.ts";

/** Creates a capability and compensates an uncertain failed request. */
export async function createComposerSecretForMark(
  client: LycaonClient,
  sessionId: string,
  projectId: string,
  request: CreateComposerSecretRequest,
): Promise<ManagedSecret> {
  try {
    return await client.createComposerSecret(sessionId, request);
  } catch (error) {
    const committed = await client
      .createComposerSecret(sessionId, request)
      .catch(() => null);
    if (!committed) {
      throw new MarkSecretFailure(SECRET_SPAN_COPY.markSecretMayBeActive);
    }
    await revokeComposerSecret(client, projectId, committed.reference);
    throw error;
  }
}

/** Revokes a capability after marking fails. */
export async function revokeComposerSecret(
  client: LycaonClient,
  projectId: string,
  reference: string,
): Promise<void> {
  const id = managedSecretId(reference);
  if (!id) throw new MarkSecretFailure(SECRET_SPAN_COPY.markLeftSecretActive);
  try {
    await client.revokeProjectManagedSecret(projectId, id);
  } catch {
    throw new MarkSecretFailure(SECRET_SPAN_COPY.markLeftSecretActive);
  }
}
