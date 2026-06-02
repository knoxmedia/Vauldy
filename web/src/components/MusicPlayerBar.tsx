import {
  CaretRightOutlined,
  CustomerServiceOutlined,
  PauseOutlined,
  StepBackwardOutlined,
  StepForwardOutlined,
  UpOutlined,
} from "@ant-design/icons";
import { Slider, Tooltip } from "antd";
import { useCallback, useEffect, useRef } from "react";
import { albumArtworkSrc, mediaPlaySrc, reportPlaybackEnd, reportPlaybackStart, savePlaybackProgress } from "../api/client";
import { isLikelyNaturalPlaybackEnd } from "../lib/playbackComplete";
import MusicFullscreenPlayer from "./MusicFullscreenPlayer";
import MusicPlayModeIcon, { MUSIC_PLAY_MODE_LABELS } from "./MusicPlayModeIcon";
import { currentMusicTrack, useMusicPlayerStore } from "../store/musicPlayer";
import { useT } from "../i18n";
import styles from "./MusicPlayerBar.module.css";

const MODE_LABELS = MUSIC_PLAY_MODE_LABELS;

function fmtTime(sec: number): string {
  if (!Number.isFinite(sec) || sec < 0) return "0:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${String(s).padStart(2, "0")}`;
}

export default function MusicPlayerBar() {
  const t = useT();
  const audioRef = useRef<HTMLAudioElement | null>(null);
  const startedRef = useRef(false);
  const lastSaveRef = useRef(0);
  const endedHandledRef = useRef(false);
  const loadingTrackRef = useRef(false);
  const programmaticPauseRef = useRef(false);
  const fullscreenOpen = useMusicPlayerStore((s) => s.fullscreen);
  const openFullscreen = useMusicPlayerStore((s) => s.openFullscreen);
  const closeFullscreen = useMusicPlayerStore((s) => s.closeFullscreen);

  const active = useMusicPlayerStore((s) => s.active);
  const playing = useMusicPlayerStore((s) => s.playing);
  const position = useMusicPlayerStore((s) => s.position);
  const duration = useMusicPlayerStore((s) => s.duration);
  const volume = useMusicPlayerStore((s) => s.volume);
  const muted = useMusicPlayerStore((s) => s.muted);
  const playMode = useMusicPlayerStore((s) => s.playMode);
  const queueIndex = useMusicPlayerStore((s) => s.queueIndex);
  const queue = useMusicPlayerStore((s) => s.queue);
  const pause = useMusicPlayerStore((s) => s.pause);
  const toggle = useMusicPlayerStore((s) => s.toggle);
  const next = useMusicPlayerStore((s) => s.next);
  const prev = useMusicPlayerStore((s) => s.prev);
  const stop = useMusicPlayerStore((s) => s.stop);
  const seek = useMusicPlayerStore((s) => s.seek);
  const setVolume = useMusicPlayerStore((s) => s.setVolume);
  const toggleMute = useMusicPlayerStore((s) => s.toggleMute);
  const cyclePlayMode = useMusicPlayerStore((s) => s.cyclePlayMode);
  const onTrackEnded = useMusicPlayerStore((s) => s.onTrackEnded);
  const syncPosition = useMusicPlayerStore((s) => s.syncPosition);
  const setPlaying = useMusicPlayerStore((s) => s.setPlaying);
  const replayToken = useMusicPlayerStore((s) => s.replayToken);

  const track = currentMusicTrack(useMusicPlayerStore.getState());

  const loadTrack = useCallback(async () => {
    const audio = audioRef.current;
    const t = currentMusicTrack(useMusicPlayerStore.getState());
    if (!audio || !t) return;
    endedHandledRef.current = false;
    startedRef.current = false;
    const src = mediaPlaySrc(t.mediaId);
    const absSrc = new URL(src, window.location.origin).href;
    if (audio.src === absSrc) return;
    loadingTrackRef.current = true;
    audio.src = src;
    audio.load();
  }, []);

  useEffect(() => {
    void loadTrack();
  }, [track?.mediaId, queueIndex, loadTrack]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    audio.volume = muted ? 0 : volume;
  }, [volume, muted]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !track) return;
    if (!playing) {
      programmaticPauseRef.current = true;
      audio.pause();
      programmaticPauseRef.current = false;
      return;
    }
    void audio.play().catch(() => {
      if (useMusicPlayerStore.getState().playing) {
        setPlaying(false);
      }
    });
  }, [playing, track?.mediaId, setPlaying]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || replayToken === 0) return;
    endedHandledRef.current = false;
    startedRef.current = false;
    audio.currentTime = 0;
    if (useMusicPlayerStore.getState().playing) {
      void audio.play().catch(() => setPlaying(false));
    }
  }, [replayToken, setPlaying]);

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !track) return;
    if (Math.abs(audio.currentTime - position) > 1.5) {
      audio.currentTime = position;
    }
  }, [position, track?.mediaId]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!active) return;
      const tag = (e.target as HTMLElement | null)?.tagName?.toLowerCase();
      if (tag === "input" || tag === "textarea" || (e.target as HTMLElement)?.isContentEditable) return;
      if (e.code === "Space") {
        e.preventDefault();
        toggle();
      } else if (e.code === "ArrowRight" && e.shiftKey) {
        e.preventDefault();
        next();
      } else if (e.code === "ArrowLeft" && e.shiftKey) {
        e.preventDefault();
        prev();
      } else if (e.code === "ArrowUp") {
        e.preventDefault();
        setVolume(Math.min(1, volume + 0.05));
      } else if (e.code === "ArrowDown") {
        e.preventDefault();
        setVolume(Math.max(0, volume - 0.05));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [active, toggle, next, prev, setVolume, volume]);

  if (!active || !track) return null;

  const progress = duration > 0 ? Math.min(100, (position / duration) * 100) : 0;

  function handleStop() {
    const audio = audioRef.current;
    if (audio) {
      programmaticPauseRef.current = true;
      audio.pause();
      audio.removeAttribute("src");
      audio.load();
      programmaticPauseRef.current = false;
    }
    endedHandledRef.current = true;
    startedRef.current = false;
    closeFullscreen();
    stop();
  }

  return (
    <>
      {!fullscreenOpen ? (
        <div className={styles.bar} role="region" aria-label={t("components.music_player_bar.aria_region")}>
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
              onChange={(e) => {
                const v = Number(e.target.value);
                seek(v);
                if (audioRef.current) audioRef.current.currentTime = v;
              }}
            />
          </div>

          <div className={styles.inner}>
            <div className={styles.left}>
              <button
                type="button"
                className={styles.coverBtn}
                onClick={() => openFullscreen()}
                aria-label={t("components.music_player_bar.aria_fullscreen")}
              >
                <img
                  src={albumArtworkSrc(track.albumId)}
                  alt=""
                  className={styles.cover}
                  onError={(e) => {
                    e.currentTarget.style.display = "none";
                  }}
                />
                <span className={styles.coverFallback}>
                  <CustomerServiceOutlined />
                </span>
                <span className={styles.coverExpand} aria-hidden>
                  <UpOutlined />
                </span>
              </button>
              <div className={styles.meta}>
                <div className={styles.title}>{track.title}</div>
                <div className={styles.subtitle}>
                  {track.artist} — {track.albumTitle}
                </div>
              </div>
            </div>

            <div className={styles.center}>
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
              <div className={styles.timeRow}>
                <span>{fmtTime(position)}</span>
                <span className={styles.timeSep}>/</span>
                <span>{fmtTime(duration)}</span>
              </div>
            </div>

            <div className={styles.right}>
              <Tooltip title={MODE_LABELS[playMode]}>
                <button type="button" className={styles.iconBtn} onClick={cyclePlayMode} aria-label={MODE_LABELS[playMode]}>
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
      ) : (
        <MusicFullscreenPlayer onClose={() => closeFullscreen()} />
      )}

      <audio
        ref={audioRef}
        preload="metadata"
        onTimeUpdate={() => {
          const audio = audioRef.current;
          const t = currentMusicTrack(useMusicPlayerStore.getState());
          if (!audio || !t) return;
          syncPosition(audio.currentTime, audio.duration || duration);
          if (!startedRef.current && audio.currentTime > 0.5) {
            startedRef.current = true;
            void reportPlaybackStart(t.mediaId, { position: Math.floor(audio.currentTime), completed: 0 }).catch(() => {});
          }
          const now = Date.now();
          if (now - lastSaveRef.current > 9000) {
            lastSaveRef.current = now;
            void savePlaybackProgress(t.mediaId, { position: Math.floor(audio.currentTime), completed: 0 }).catch(() => {});
          }
        }}
        onLoadedMetadata={() => {
          const audio = audioRef.current;
          if (!audio) return;
          loadingTrackRef.current = false;
          syncPosition(audio.currentTime, audio.duration || 0);
        }}
        onCanPlay={() => {
          loadingTrackRef.current = false;
        }}
        onEnded={() => {
          if (endedHandledRef.current) return;
          const audio = audioRef.current;
          const t = currentMusicTrack(useMusicPlayerStore.getState());
          if (!audio || !t) return;
          if (
            !isLikelyNaturalPlaybackEnd(
              startedRef.current,
              audio.currentTime,
              audio.duration || duration,
              true,
            )
          ) {
            return;
          }
          endedHandledRef.current = true;
          void savePlaybackProgress(t.mediaId, { position: Math.floor(audio.duration || 0), completed: 1 }).catch(() => {});
          void reportPlaybackEnd(t.mediaId, { position: Math.floor(audio.duration || 0), completed: 1 }).catch(() => {});
          onTrackEnded();
        }}
        onPlay={() => {
          /* playing state is driven by the store; ignore element events to avoid feedback loops */
        }}
        onPause={() => {
          if (endedHandledRef.current || loadingTrackRef.current || programmaticPauseRef.current) return;
          if (useMusicPlayerStore.getState().playing) {
            setPlaying(false);
          }
        }}
        onError={() => {
          loadingTrackRef.current = false;
          pause();
        }}
      />
    </>
  );
}
