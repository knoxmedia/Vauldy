import { create } from "zustand";
import { persist } from "zustand/middleware";

export type UserRole = "admin" | "user" | "api_client";

type AuthState = {
  token: string | null;
  role: UserRole | null;
  username: string | null;
  setToken: (t: string | null) => void;
  setProfile: (username: string, role: UserRole) => void;
  clearSession: () => void;
};

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      role: null,
      username: null,
      setToken: (t) => set({ token: t }),
      setProfile: (username, role) => set({ username, role }),
      clearSession: () => set({ token: null, role: null, username: null }),
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
