import { create } from "zustand";
import { persist } from "zustand/middleware";

export type UserRole = "admin" | "user" | "api_client";

type AuthState = {
  token: string | null;
  role: UserRole | null;
  username: string | null;
  /** From GET /user/info; null until loaded for this session. */
  canPlay: boolean | null;
  setToken: (t: string | null) => void;
  setProfile: (username: string, role: UserRole, caps?: { canPlay?: boolean }) => void;
  clearSession: () => void;
};

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      role: null,
      username: null,
      canPlay: null,
      setToken: (t) => set({ token: t }),
      setProfile: (username, role, caps) =>
        set({
          username,
          role,
          ...(caps?.canPlay !== undefined ? { canPlay: caps.canPlay } : {}),
        }),
      clearSession: () => set({ token: null, role: null, username: null, canPlay: null }),
    }),
    {
      name: "knox-media-auth",
      partialize: (s) => ({
        token: s.token,
        role: s.role,
        username: s.username,
      }),
    }
  )
);

export function isAdminRole(role: UserRole | null | undefined) {
  return role === "admin";
}
