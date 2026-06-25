import { create } from "zustand";

export type BrandingState = {
  appName: string;
  faviconUrl: string;
  loaded: boolean;
  load: () => Promise<void>;
};

const DEFAULT_APP_NAME = "Knox-Media";

function applyBrandingToDocument(appName: string, faviconUrl: string) {
  if (typeof document === "undefined") return;
  document.title = appName;
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (!link) {
    link = document.createElement("link");
    link.rel = "icon";
    document.head.appendChild(link);
  }
  const ext = faviconUrl.split("?")[0].split(".").pop()?.toLowerCase();
  link.type =
    ext === "png"
      ? "image/png"
      : ext === "ico"
        ? "image/x-icon"
        : "image/svg+xml";
  link.href = faviconUrl;
}

export const useBrandingStore = create<BrandingState>((set) => ({
  appName: DEFAULT_APP_NAME,
  faviconUrl: "/favicon.svg",
  loaded: false,
  load: async () => {
    try {
      const res = await fetch("/api/v1/branding");
      if (!res.ok) return;
      const data = (await res.json()) as { app_name?: string; favicon_url?: string };
      const appName = (data.app_name || DEFAULT_APP_NAME).trim() || DEFAULT_APP_NAME;
      const faviconUrl = (data.favicon_url || "/favicon.svg").trim() || "/favicon.svg";
      applyBrandingToDocument(appName, faviconUrl);
      set({ appName, faviconUrl, loaded: true });
    } catch {
      set({ loaded: true });
    }
  },
}));

export function useAppName(): string {
  return useBrandingStore((s) => s.appName);
}
