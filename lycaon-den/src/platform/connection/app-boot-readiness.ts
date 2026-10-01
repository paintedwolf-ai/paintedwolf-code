import { createPreparation } from "../../ui/presentation.ts";

// Cached chats hold sends until discovery and reconciliation finish.
export const appBootPreparation = createPreparation();
