import { setupServer } from "msw/node";
import { lycaonHandlers } from "./handlers.ts";

export const mswServer = setupServer(...lycaonHandlers);
