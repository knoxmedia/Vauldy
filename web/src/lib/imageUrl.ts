import { useAuthStore } from "../store/auth";

/** Hosts that block direct browser loads without a provider Referer header. */
function needsImageProxy(url: string): boolean {
  try {
    const host = new URL(url, window.location.origin).hostname.toLowerCase();
    return (
      host.includes("douban.com") ||
      host.includes("doubanio.com") ||
      host.includes("bangumi.tv") ||
      host.includes("bgm.tv") ||
      host.includes("fanart.tv") ||
      host.includes("tmdb.org") ||
      host.includes("themoviedb.org")
    );
  } catch {
    return false;
  }
}

/** Build an authenticated proxy URL for remote scrape posters when hotlinking is blocked. */
export function proxyImageSrc(raw: string): string {
  const u = (raw || "").trim();
  if (!u || !needsImageProxy(u)) return u;
  const params = new URLSearchParams({ url: u });
  const token = useAuthStore.getState().token;
  if (token) params.set("access_token", token);
  return `/api/v1/proxy/image?${params.toString()}`;
}
