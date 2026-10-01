import { splitProps, type ComponentProps } from "solid-js";
import { Portal } from "solid-js/web";
import { useResidentInteractive, useResidentPresence } from "../../ui/resident-presence-context.tsx";

/** Portaled UI follows the visibility of its retained surface. */
export function ResidentPortal(props: ComponentProps<typeof Portal>) {
  const [local, portal] = splitProps(props, ["children"]);
  const presence = useResidentPresence();
  const interactive = useResidentInteractive();
  const hidden = () => !interactive();
  return (
    <Portal {...portal}>
      <div data-resident-portal={presence()} style={{ display: hidden() ? "none" : "contents" }}
        aria-hidden={hidden() ? true : undefined} inert={hidden() ? true : undefined}>
        {local.children}
      </div>
    </Portal>
  );
}
