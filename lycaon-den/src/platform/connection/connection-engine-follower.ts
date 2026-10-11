import type { AppStore } from "../../store/app-state-model.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { readSidecarInfo, type BackendConnection } from "./backend.ts";
import { watchEngineState } from "./engine-supervision.ts";

const RESTARTING_NOTICE = "engine_restarting";

type EngineFollowerPorts = {
  app: AppStore;
  notices: NoticeStore | null;
  connecting(): boolean;
  boundGeneration(): number | undefined;
  connect(connection: BackendConnection): Promise<unknown>;
};

/** Subscription setup and sidecar discovery remain cancellable until they settle. */
export function createEngineFollower(ports: EngineFollowerPorts) {
  let stopped = false;
  let stopSubscription: (() => void) | undefined;
  let sawEngineDown = false;
  const ready = watchEngineState((engine) => {
    if (stopped) return;
    if (engine.state === "restarting") {
      sawEngineDown = true;
      ports.app.actions.setSidecarStatus("disconnected");
      ports.notices?.reporterFor(APP_SCOPE).publish({
        code: RESTARTING_NOTICE,
        severity: "warning",
        title: CLIENT_NOTICES.engine_restarting.title,
        message: CLIENT_NOTICES.engine_restarting.message,
      });
      return;
    }
    ports.notices?.withdraw(RESTARTING_NOTICE, APP_SCOPE);
    if (engine.state === "stopped") {
      sawEngineDown = true;
      ports.app.actions.setSidecarStatus("disconnected");
      return;
    }
    if (engine.state !== "running" || ports.connecting()) return;
    const bound = ports.boundGeneration();
    // Initial boot establishes its own connection.
    if (bound === engine.generation || (bound === undefined && !sawEngineDown)) return;
    sawEngineDown = false;
    void readSidecarInfo().then((connection) => {
      if (stopped || !connection || connection.engineGeneration !== engine.generation) return;
      if (ports.boundGeneration() === engine.generation) return;
      return ports.connect(connection);
    }).catch(() => undefined);
  }).then((stop) => {
    if (stopped) stop();
    else stopSubscription = stop;
  });
  return {
    ready,
    dispose: () => {
      if (stopped) return;
      stopped = true;
      stopSubscription?.();
      stopSubscription = undefined;
    },
  };
}
