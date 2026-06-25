import {
  CaretRightOutlined,
  CustomerServiceOutlined,
  DownOutlined,
  PauseOutlined,
  StepBackwardOutlined,
  StepForwardOutlined,
} from "@ant-design/icons";
import { Slider, Tooltip } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { albumArtworkSrc, fetchMediaLyrics } from "../api/client";
import { activeLrcIndex, parseLrc } from "../lib/lrc";
import MusicPlayModeIcon, { MUSIC_PLAY_MODE_LABELS } from "./MusicPlayModeIcon";
import { currentMusicTrack, useMusicPlayerStore } from "../store/musicPlayer";
import { useT } from "../i18n";
import styles from "./MusicFullscreenPlayer.module.css";

type Props = {
  onClose: () => void;
};

function fmtTime(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return "0:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${String(s).padStart(2, "0")}`;
}

export default function MusicFullscreenPlayer({ onClose }: Props) {
  const t = useT();
  const lyricsRef = useRef<HTMLDivElement | null>(null);
  const lineRefs = useRef<Array<HTMLParagraphElement | null>>([]);
  const [lrcRaw, setLrcRaw] = useState("");

  const playing = useMusicPlayerStore((s) => s.playing);
  const position = useMusicPlayerStore((s) => s.position);
  const duration = useMusicPlayerStore((s) => s.duration);
  const volume = useMusicPlayerStore((s) => s.volume);
  const muted = useMusicPlayerStore((s) => s.muted);
  const playMode = useMusicPlayerStore((s) => s.playMode);
  const queueIndex = useMusicPlayerStore((s) => s.queueIndex);
  const queue = useMusicPlayerStore((s) => s.queue);
  const toggle = useMusicPlayerStore((s) => s.toggle);
  const next = useMusicPlayerStore((s) => s.next);
  const prev = useMusicPlayerStore((s) => s.prev);
  const stop = useMusicPlayerStore((s) => s.stop);
  const seek = useMusicPlayerStore((s) => s.seek);
  const setVolume = useMusicPlayerStore((s) => s.setVolume);
  const toggleMute = useMusicPlayerStore((s) => s.toggleMute);
  const cyclePlayMode = useMusicPlayerStore((s) => s.cyclePlayMode);

  const track = currentMusicTrack(useMusicPlayerStore.getState());
  const lines = useMemo(() => parseLrc(lrcRaw), [lrcRaw]);
  const activeIdx = activeLrcIndex(lines, position);
  const progress = duration > 0 ? Math.min(100, (position / duration) * 100) : 0;

  useEffect(() => {
    if (!track) return;
    let cancelled = false;
    setLrcRaw("");
    void fetchMediaLyrics(track.mediaId)
      .then((res) => {
        if (!cancelled) setLrcRaw(res?.lrc ?? "");
      })
      .catch(() => {
        if (!cancelled) setLrcRaw("");
      });
    return () => {
      cancelled = true;
    };
  }, [track?.mediaId]);

  useEffect(() => {
    const el = activeIdx >= 0 ? lineRefs.current[activeIdx] : null;
    if (!el || !lyricsRef.current) return;
    el.scrollIntoView({ block: "center", behavior: playing ? "smooth" : "auto" });
  }, [activeIdx, playing]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.code === "Escape") {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  if (!track) return null;

  function handleStop() {
    stop();
    onClose();
  }

  return (
    <div className={styles.overlay} role="dialog" aria-label={t("components.music_fullscreen_player.aria_dialog")}>
      <div className={styles.topBar}>
        <button type="button" className={styles.topBtn} onClick={onClose} aria-label={t("components.music_fullscreen_player.aria_collapse")}>
          <DownOutlined />
        </button>
      </div>

      <div className={`${styles.main} ${lines.length > 0 ? styles.mainWithLyrics : ""}`}>
        <div className={styles.artBlock}>
          <div className={styles.coverWrap}>
            <img
              src={albumArtworkSrc(track.albumId)}
              alt=""
              className={styles.coverImg}
              onError={(e) => {
                e.currentTarget.style.display = "none";
              }}
            />
            <div className={styles.coverFallback}>
              <CustomerServiceOutlined />
            </div>
          </div>
          <div className={styles.trackTitle}>{track.title}</div>
          <div className={styles.trackSub}>
            {track.artist} — {track.albumTitle}
          </div>
        </div>

        {lines.length > 0 ? (
          <div className={styles.lyricsPanel} ref={lyricsRef}>
            {lines.map((line, idx) => (
              <p
                key={`${line.timeSec}-${idx}-${line.text}`}
                ref={(el) => {
                  lineRefs.current[idx] = el;
                }}
                className={`${styles.lyricLine} ${idx === activeIdx ? styles.lyricActive : ""} ${idx < activeIdx ? styles.lyricPast : ""}`}
              >
                {line.text}
              </p>
            ))}
          </div>
        ) : null}
      </div>

      <div className={styles.bottom}>
        <div className={styles.progressTrack}>
          <div className={styles.progressFill} style={{ width: `${progress}%` }} />
          <input
            type="range"
            min={0}
            max={duration > 0 ? duration : 100}
            step={1}
            value={Math.min(position, duration || 0)}
            className={styles.progressInput}
            aria-label={t("components.music_player_bar.aria_progress")}
            onChange={(e) => seek(Number(e.target.value))}
          />
        </div>

        <div className={styles.bottomInner}>
          <div className={styles.bottomMeta}>
            <div className={styles.bottomTitle}>{track.title}</div>
            <div className={styles.bottomSub}>
              {track.artist} — {track.albumTitle}
            </div>
            <div className={styles.bottomTime}>
              {fmtTime(position)} / {fmtTime(duration)}
            </div>
          </div>

          <div className={styles.controls}>
            <Tooltip title={t("components.music_player_bar.tooltip_prev")}>
              <button type="button" className={styles.iconBtn} onClick={prev} aria-label={t("components.music_player_bar.aria_prev")}>
                <StepBackwardOutlined />
              </button>
            </Tooltip>
            <button type="button" className={styles.playBtn} onClick={toggle} aria-label={playing ? t("components.music_player_bar.aria_pause") : t("components.music_player_bar.aria_play")}>
              {playing ? <PauseOutlined /> : <CaretRightOutlined />}
            </button>
            <Tooltip title={t("components.music_player_bar.tooltip_next")}>
              <button type="button" className={styles.iconBtn} onClick={next} aria-label={t("components.music_player_bar.aria_next")}>
                <StepForwardOutlined />
              </button>
            </Tooltip>
            <Tooltip title={t("components.music_player_bar.tooltip_stop")}>
              <button type="button" className={styles.iconBtn} onClick={handleStop} aria-label={t("components.music_player_bar.aria_stop")}>
                <span className={styles.stopIcon} aria-hidden />
              </button>
            </Tooltip>
          </div>

          <div className={styles.bottomRight}>
            <Tooltip title={MUSIC_PLAY_MODE_LABELS[playMode]}>
              <button type="button" className={styles.iconBtn} onClick={cyclePlayMode} aria-label={MUSIC_PLAY_MODE_LABELS[playMode]}>
                <MusicPlayModeIcon mode={playMode} className={styles.modeIcon} />
              </button>
            </Tooltip>
            <div className={styles.volume}>
              <Slider
                min={0}
                max={1}
                step={0.01}
                value={muted ? 0 : volume}
                onChange={setVolume}
                tooltip={{ formatter: (v) => `${Math.round((v ?? 0) * 100)}%` }}
                style={{ width: 96 }}
              />
              <button type="button" className={styles.iconBtn} onClick={toggleMute} aria-label={muted ? t("components.music_player_bar.aria_unmute") : t("components.music_player_bar.aria_mute")}>
                {muted || volume === 0 ? "🔇" : "🔊"}
              </button>
            </div>
            <span className={styles.queueHint}>
              {queueIndex + 1}/{queue.length}
            </span>
          </div>
        </div>
      </div>
    </div>
  );
}
