import { FindBar } from "./FindBar.tsx";
import { GotoLineBar } from "./GotoLineBar.tsx";

export function EditorCommandBar() {
  return (
    <>
      <GotoLineBar />
      <FindBar />
    </>
  );
}
