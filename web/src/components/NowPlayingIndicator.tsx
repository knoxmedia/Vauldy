import { useT } from "../i18n";
import styles from "./NowPlayingIndicator.module.css";

type Props = {
  playing: boolean;
  className?: string;
};

/** Animated equalizer bars shown on the currently playing track row. */
export default function NowPlayingIndicator({ playing, className }: Props) {
  const t = useT();
  return (
    <span
      className={[styles.wrap, playing ? styles.animate : styles.paused, className].filter(Boolean).join(" ")}
      role="img"
      aria-label={playing ? t("components.now_playing_indicator.playing") : t("components.now_playing_indicator.paused")}
    >
      <span className={styles.bar} />
      <span className={styles.bar} />
      <span className={styles.bar} />
    </span>
  );
}
