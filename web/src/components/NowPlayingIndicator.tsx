import styles from "./NowPlayingIndicator.module.css";

type Props = {
  playing: boolean;
  className?: string;
};

/** Animated equalizer bars shown on the currently playing track row. */
export default function NowPlayingIndicator({ playing, className }: Props) {
  return (
    <span
      className={[styles.wrap, playing ? styles.animate : styles.paused, className].filter(Boolean).join(" ")}
      role="img"
      aria-label={playing ? "正在播放" : "已暂停"}
    >
      <span className={styles.bar} />
      <span className={styles.bar} />
      <span className={styles.bar} />
    </span>
  );
}
