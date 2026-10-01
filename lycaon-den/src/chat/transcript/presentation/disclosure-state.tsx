import { createContext, createSignal, useContext } from "solid-js";
import type { TranscriptDisclosureKey } from "./transcript-disclosure-key.ts";

export type TranscriptDisclosureStore = {
  isOpen: (key: TranscriptDisclosureKey) => boolean;
  setUserOpen: (key: TranscriptDisclosureKey, open: boolean) => void;
  acquire: (key: TranscriptDisclosureKey, lease?: symbol) => () => void;
  userOpenKeys: () => TranscriptDisclosureKey[];
  restoreUserOpenKeys: (keys: readonly TranscriptDisclosureKey[]) => void;
};

/** Disclosure intent belongs to the transcript, independently of mounted rows. */
export function createTranscriptDisclosureStore(onUserChange?: () => void): TranscriptDisclosureStore {
  const rows = new Map<TranscriptDisclosureKey, { userOpen: boolean; leases: Set<symbol> }>();
  const [version, setVersion] = createSignal(0);
  const changed = () => setVersion(value=>value+1);
  const ensure = (key: TranscriptDisclosureKey) => {
    let row = rows.get(key);
    if (!row) { row = { userOpen:false, leases:new Set() }; rows.set(key,row); }
    return row;
  };
  const prune = (key: TranscriptDisclosureKey) => { const row=rows.get(key); if (row && !row.userOpen && !row.leases.size) rows.delete(key); };
  return {
    isOpen: key => { version(); const row=rows.get(key); return row?.userOpen===true || Boolean(row?.leases.size); },
    setUserOpen: (key,open) => {
      const row=ensure(key); row.userOpen=open; row.leases.clear(); prune(key); changed(); onUserChange?.();
    },
    acquire: (key,requestedLease) => {
      const lease=requestedLease ?? Symbol("disclosure");
      ensure(key).leases.add(lease); changed();
      let released=false;
      return ()=>{ if (released) return; released=true; if (!rows.get(key)?.leases.delete(lease)) return; prune(key); changed(); };
    },
    userOpenKeys: ()=>{ version(); return [...rows].filter(([,row])=>row.userOpen).map(([key])=>key); },
    restoreUserOpenKeys: keys=>{ rows.clear(); for (const key of keys) rows.set(key,{ userOpen:true,leases:new Set() }); changed(); },
  };
}
const DisclosureContext = createContext<TranscriptDisclosureStore>();
export const TranscriptDisclosureProvider = DisclosureContext.Provider;
export const useTranscriptDisclosureStore = () => useContext(DisclosureContext);
