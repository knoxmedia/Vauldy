import type { SVGProps } from "react";
import type { MusicPlayMode } from "../store/musicPlayer";
import { tGlobal } from "../i18n";

type Props = SVGProps<SVGSVGElement> & {
  mode: MusicPlayMode;
};

/** Playback mode icons (Plex / Spotify style). */
export default function MusicPlayModeIcon({ mode, className, ...rest }: Props) {
  const common = {
    xmlns: "http://www.w3.org/2000/svg",
    viewBox: "0 0 24 24",
    fill: "currentColor",
    className,
    "aria-hidden": true as const,
    ...rest,
  };

  switch (mode) {
    case "sequential":
      return (
        <svg {...common}>
          <path d="M4 5h11v2H4V5zm0 4.5h11v2H4v-2zm0 4.5h11v2H4v-2z" />
          <path d="M18.5 8.5 22 12l-3.5 3.5V8.5z" />
        </svg>
      );
    case "repeat-all":
      return (
        <svg {...common}>
          <path d="M7 6h9v3l4-4-4-4v3H5v6h2V6zm10 12H8v-3l-4 4 4 4v-3h9v-6h-2v4z" />
        </svg>
      );
    case "repeat-one":
      return (
        <svg {...common}>
          <path d="M7 6h9v3l4-4-4-4v3H5v6h2V6zm10 12H8v-3l-4 4 4 4v-3h9v-6h-2v4z" />
          <text x="12" y="13.8" fontSize="7.5" fontWeight="700" textAnchor="middle" fill="currentColor">
            1
          </text>
        </svg>
      );
    case "shuffle":
      return (
        <svg {...common}>
          <path d="M16.3 5.5 15 4.2 7.4 11.8 4.6 9 3.2 10.4 7.4 14.6 16.3 5.5zM16.3 18.5 7.4 9.4 3.2 13.6 4.6 15 7.4 12.2 15 19.8l1.3-1.3zM20 9h-8v2h8V9zM4 15h8v-2H4v2z" />
        </svg>
      );
    default:
      return null;
  }
}

/**
 * Reactive playback-mode labels.
 *
 * Reads from the active locale at access time (via tGlobal), so refreshing the
 * UI after a language change picks up the new translations automatically.
 */
export const MUSIC_PLAY_MODE_LABELS = new Proxy({} as Record<MusicPlayMode, string>, {
  get(_target, prop: string): string {
    return tGlobal(`components.music_play_mode.${prop}`);
  },
});
