import { create } from "zustand";
import { persist } from "zustand/middleware";

import type { PlayerPrefs } from "../lib/playerPrefs";

export type UserRole = "admin" | "user" | "api_client";

type AuthState = {
  token: string | null;
  role: UserRole | null;
  username: string | null;
  /** From GET /user/info; null until loaded for this session. */
  canPlay: boolean | null;
  avatarUrl: string | null;
  uiLocale: string | null;
  playerPrefs: PlayerPrefs | null;
  setToken: (t: string | null) => void;
  setProfile: (
    username: string,
    role: UserRole,
    caps?: { canPlay?: boolean; avatarUrl?: string | null; uiLocale?: string | null; playerPrefs?: PlayerPrefs | null }
  ) => void;
  clearSession: () => void;
};

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      token: null,
      role: null,
      username: null,
      canPlay: null,
      avatarUrl: null,
      uiLocale: null,
      playerPrefs: null,
      setToken: (t) => set({ token: t }),
      setProfile: (username, role, caps) =>
        set({
          username,
          role,
          ...(caps?.canPlay !== undefined ? { canPlay: caps.canPlay } : {}),
          ...(caps?.avatarUrl !== undefined ? { avatarUrl: caps.avatarUrl } : {}),
          ...(caps?.uiLocale !== undefined ? { uiLocale: caps.uiLocale } : {}),
          ...(caps?.playerPrefs !== undefined ? { playerPrefs: caps.playerPrefs } : {}),
        }),
      clearSession: () =>
        set({
          token: null,
          role: null,
          username: null,
          canPlay: null,
          avatarUrl: null,
          uiLocale: null,
          playerPrefs: null,
        }),
    }),
    {
      name: "knox-media-auth",
      partialize: (s) => ({
        token: s.token,
        role: s.role,
        username: s.username,
        avatarUrl: s.avatarUrl,
        uiLocale: s.uiLocale,
        playerPrefs: s.playerPrefs,
      }),
    }
  )
);

export function isAdminRole(role: UserRole | null | undefined) {
  return role === "admin";
}
