/** Knox media server serves this path (see api/router.go); Vite proxies /static in dev. */
const POWERPLAYER_SCRIPT = "/static/powerplayer6/powerplayer.min.js";

/** Injects PowerPlayer once into <head>; does not block the React app. */
export function loadPowerPlayerScript(): void {
  if (typeof document === "undefined") return;
  if (document.querySelector('script[data-knox-powerplayer="1"]')) return;
  const el = document.createElement("script");
  el.src = POWERPLAYER_SCRIPT;
  el.async = true;
  el.dataset.knoxPowerplayer = "1";
  el.onerror = () => {
    console.warn("[knox] PowerPlayer script failed to load:", POWERPLAYER_SCRIPT);
  };
  document.head.appendChild(el);
}
