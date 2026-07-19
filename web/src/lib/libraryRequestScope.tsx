import { createContext, useContext, useEffect, useMemo, type ReactNode } from "react";
import { fetchLibrariesWithCapabilities } from "../api/client";
import { useAuthStore } from "../store/auth";
import { createLibraryRequestScope } from "./sharedLibraryRequest";

type Result = Awaited<ReturnType<typeof fetchLibrariesWithCapabilities>>;
type Scope = { scopeId: string; load(signal?: AbortSignal): Promise<Result> };
let nextScopeId = 0;
const Context = createContext<Scope | null>(null);

export function LibraryRequestScopeProvider({ children }: { children: ReactNode }) {
  const token = useAuthStore((state) => state.token);
  const username = useAuthStore((state) => state.username);
  // A scope object is recreated for the exact auth generation; no token is stored in a module map or logged.
  const scope = useMemo(() => Object.assign(createLibraryRequestScope(fetchLibrariesWithCapabilities), { scopeId: `library-scope-${++nextScopeId}` }), [username, token]);
  useEffect(() => scope.acquireOwner(), [scope]);
  return <Context.Provider value={scope}>{children}</Context.Provider>;
}

export function useLibraryRequestScope(): Scope | null {
  return useContext(Context);
}
