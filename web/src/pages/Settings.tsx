import { Checkbox, Select, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Cropper, { type Area } from "react-easy-crop";
import "react-easy-crop/react-easy-crop.css";

import {
  changeUserPassword,
  deleteUserAvatar,
  fetchUserInfo,
  updateUserProfile,
  uploadUserAvatar,
} from "../api/client";
import { languageOptions, resolveLocale, useT, type TranslateFn } from "../i18n";
import { defaultPlayerPrefs, normalizePlayerPrefs, type PlayerPrefs } from "../lib/playerPrefs";
import { isAdminRole, useAuthStore } from "../store/auth";
import { getCroppedCircularPngBlob } from "../utils/cropImage";
import {
  BG_OPACITY_OPTIONS,
  POS_PCT_OPTIONS,
  buildBgColorOptions,
  buildShadowOptions,
  buildTextColorOptions,
  buildTextSizeOptions,
  normalizeSubtitleAppearance,
  previewSubtitleBoxStyle,
  summarizeSubtitleAppearance,
  type SubtitleAppearance,
} from "../lib/subtitleAppearance";
import styles from "./Settings.module.css";

type EditMode = null | "avatar" | "language" | "audio" | "password" | "subtitle";

const TRACK_LANG_VALUES = ["zh", "en", "ja", "ko", "fr", "de", "es", "ru"] as const;

function buildTrackLangOptions(t: TranslateFn): { value: string; label: string }[] {
  const labels: Record<(typeof TRACK_LANG_VALUES)[number], string> = {
    zh: t("languages.zh-CN"),
    en: t("languages.en"),
    ja: t("languages.ja"),
    ko: t("languages.ko"),
    fr: "Français",
    de: "Deutsch",
    es: "Español",
    ru: "Русский",
  };
  return [
    { value: "", label: t("settings.audio.track_select_lang") },
    ...TRACK_LANG_VALUES.map((v) => ({ value: v, label: labels[v] })),
  ];
}

function summarizePlayerPrefsLocalized(prefs: PlayerPrefs, t: TranslateFn): string {
  const SUBTITLE_MODE_LABEL: Record<PlayerPrefs["subtitle_mode"], string> = {
    foreign: t("settings.audio.subtitle_mode_foreign"),
    always: t("settings.audio.subtitle_mode_always"),
    off: t("settings.audio.subtitle_mode_off"),
  };
  const SDH_LABEL: Record<PlayerPrefs["sdh_search"], string> = {
    prefer_non_sdh: t("settings.audio.sdh_prefer_non_sdh"),
    prefer_sdh: t("settings.audio.sdh_prefer_sdh"),
  };
  const FORCED_LABEL: Record<PlayerPrefs["forced_search"], string> = {
    prefer_non_forced: t("settings.audio.forced_prefer_non_forced"),
    prefer_forced: t("settings.audio.forced_prefer_forced"),
  };
  const auto = prefs.auto_select ? t("settings.audio.summary_auto") : t("settings.audio.summary_manual");
  const track = `${t("settings.audio.summary_track")}${SUBTITLE_MODE_LABEL[prefs.subtitle_mode]}`;
  const search = `${t("settings.audio.summary_search")}${SDH_LABEL[prefs.sdh_search]}，${FORCED_LABEL[prefs.forced_search]}`;
  return `${auto}${track}\n${search}`;
}

export default function SettingsPage() {
  const username = useAuthStore((s) => s.username);
  const role = useAuthStore((s) => s.role);
  const avatarUrl = useAuthStore((s) => s.avatarUrl);
  const uiLocale = useAuthStore((s) => s.uiLocale);
  const playerPrefs = useAuthStore((s) => s.playerPrefs);
  const setProfile = useAuthStore((s) => s.setProfile);
  const t = useT();

  const UI_LOCALES = useMemo(() => languageOptions(), []);
  const LANG_TRACKS = useMemo(() => buildTrackLangOptions(t), [t]);
  const SUBTITLE_MODE_OPTS: { value: PlayerPrefs["subtitle_mode"]; label: string }[] = useMemo(
    () => [
      { value: "foreign", label: t("settings.audio.subtitle_mode_foreign") },
      { value: "always", label: t("settings.audio.subtitle_mode_always") },
      { value: "off", label: t("settings.audio.subtitle_mode_off") },
    ],
    [t]
  );
  const SDH_OPTS: { value: PlayerPrefs["sdh_search"]; label: string }[] = useMemo(
    () => [
      { value: "prefer_non_sdh", label: t("settings.audio.sdh_prefer_non_sdh") },
      { value: "prefer_sdh", label: t("settings.audio.sdh_prefer_sdh") },
    ],
    [t]
  );
  const FORCED_OPTS: { value: PlayerPrefs["forced_search"]; label: string }[] = useMemo(
    () => [
      { value: "prefer_non_forced", label: t("settings.audio.forced_prefer_non_forced") },
      { value: "prefer_forced", label: t("settings.audio.forced_prefer_forced") },
    ],
    [t]
  );
  const subtitleTextSizeOptions = useMemo(() => buildTextSizeOptions(t), [t]);
  const subtitleTextColorOptions = useMemo(() => buildTextColorOptions(t), [t]);
  const subtitleShadowOptions = useMemo(() => buildShadowOptions(t), [t]);
  const subtitleBgColorOptions = useMemo(() => buildBgColorOptions(t), [t]);

  const uiLocaleLabel = useCallback(
    (code: string | null | undefined) => {
      const resolved = resolveLocale(code);
      return UI_LOCALES.find((x) => x.value === resolved)?.label || resolved;
    },
    [UI_LOCALES]
  );

  const [edit, setEdit] = useState<EditMode>(null);
  const [loading, setLoading] = useState(false);

  const refresh = useCallback(async () => {
    try {
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || null,
        uiLocale: u.ui_locale || null,
        playerPrefs: u.player_prefs ? normalizePlayerPrefs(u.player_prefs) : defaultPlayerPrefs(),
      });
    } catch {
      message.error(t("settings.language.load_failure"));
    }
  }, [setProfile, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const prefs = playerPrefs ?? defaultPlayerPrefs();
  const subtitleAppearanceSummary = useMemo(
    () => summarizeSubtitleAppearance(prefs.subtitle_appearance, t),
    [prefs.subtitle_appearance, t],
  );
  const loc = resolveLocale(uiLocale);

  const [langDraft, setLangDraft] = useState(loc);
  useEffect(() => {
    setLangDraft(loc);
  }, [loc, edit]);

  const [audioDraft, setAudioDraft] = useState<PlayerPrefs>(prefs);
  useEffect(() => {
    setAudioDraft(prefs);
  }, [playerPrefs, edit]);

  const [subtitleDraft, setSubtitleDraft] = useState<SubtitleAppearance>(
    normalizeSubtitleAppearance(prefs.subtitle_appearance)
  );
  useEffect(() => {
    setSubtitleDraft(normalizeSubtitleAppearance(prefs.subtitle_appearance));
  }, [playerPrefs, edit]);

  const [pw1, setPw1] = useState("");
  const [pw2, setPw2] = useState("");

  const fileRef = useRef<HTMLInputElement>(null);
  const [imgSrc, setImgSrc] = useState<string | null>(null);
  const [crop, setCrop] = useState({ x: 0, y: 0 });
  const [zoom, setZoom] = useState(1);
  const [croppedAreaPixels, setCroppedAreaPixels] = useState<Area | null>(null);

  useEffect(() => {
    if (edit !== "avatar") {
      setImgSrc((prev) => {
        if (prev?.startsWith("blob:")) URL.revokeObjectURL(prev);
        return null;
      });
      setZoom(1);
      setCrop({ x: 0, y: 0 });
      setCroppedAreaPixels(null);
    } else if (avatarUrl) {
      setImgSrc(avatarUrl);
      setZoom(1);
      setCrop({ x: 0, y: 0 });
    }
  }, [edit, avatarUrl]);

  const onPickFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    e.target.value = "";
    if (!f || !f.type.startsWith("image/")) {
      message.warning(t("settings.avatar.select_image_file"));
      return;
    }
    const url = URL.createObjectURL(f);
    setImgSrc((prev) => {
      if (prev?.startsWith("blob:")) URL.revokeObjectURL(prev);
      return url;
    });
    setZoom(1);
    setCrop({ x: 0, y: 0 });
  };

  const canSaveAvatar = !!imgSrc && !!croppedAreaPixels && !loading;

  const saveAvatar = async () => {
    if (!imgSrc || !croppedAreaPixels) return;
    setLoading(true);
    try {
      const blob = await getCroppedCircularPngBlob(imgSrc, croppedAreaPixels);
      const url = await uploadUserAvatar(blob);
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || url,
        uiLocale: u.ui_locale || null,
        playerPrefs: u.player_prefs ? normalizePlayerPrefs(u.player_prefs) : defaultPlayerPrefs(),
      });
      message.success(t("settings.avatar.saved"));
      setEdit(null);
    } catch {
      message.error(t("settings.avatar.save_failed"));
    } finally {
      setLoading(false);
    }
  };

  const removeAvatar = async () => {
    setLoading(true);
    try {
      await deleteUserAvatar();
      await refresh();
      message.success(t("settings.avatar.deleted"));
      setEdit(null);
    } catch {
      message.error(t("settings.avatar.delete_failed"));
    } finally {
      setLoading(false);
    }
  };

  const saveLanguage = async () => {
    setLoading(true);
    try {
      const data = await updateUserProfile({ ui_locale: langDraft });
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || null,
        uiLocale: data.ui_locale,
        playerPrefs: normalizePlayerPrefs(data.player_prefs),
      });
      message.success(t("settings.language.success"));
      setEdit(null);
    } catch {
      message.error(t("settings.language.failure"));
    } finally {
      setLoading(false);
    }
  };

  const saveAudio = async () => {
    setLoading(true);
    try {
      const data = await updateUserProfile({ player_prefs: audioDraft });
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || null,
        uiLocale: u.ui_locale || null,
        playerPrefs: normalizePlayerPrefs(data.player_prefs),
      });
      message.success(t("settings.audio.success"));
      setEdit(null);
    } catch {
      message.error(t("settings.audio.failure"));
    } finally {
      setLoading(false);
    }
  };

  const saveSubtitleAppearance = async () => {
    setLoading(true);
    try {
      const base = normalizePlayerPrefs(playerPrefs);
      const merged: PlayerPrefs = {
        ...base,
        subtitle_appearance: normalizeSubtitleAppearance(subtitleDraft),
      };
      const data = await updateUserProfile({ player_prefs: merged });
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || null,
        uiLocale: u.ui_locale || null,
        playerPrefs: normalizePlayerPrefs(data.player_prefs),
      });
      message.success(t("settings.subtitle_appearance.success"));
      setEdit(null);
    } catch {
      message.error(t("settings.subtitle_appearance.failure"));
    } finally {
      setLoading(false);
    }
  };

  const savePassword = async () => {
    if (pw1.length < 6) {
      message.warning(t("settings.password.too_short"));
      return;
    }
    if (pw1 !== pw2) {
      message.warning(t("settings.password.mismatch"));
      return;
    }
    setLoading(true);
    try {
      await changeUserPassword(pw1, pw2);
      message.success(t("settings.password.success"));
      setPw1("");
      setPw2("");
      setEdit(null);
    } catch {
      message.error(t("settings.password.failure"));
    } finally {
      setLoading(false);
    }
  };

  const selectDark = {
    width: "100%",
    maxWidth: 400,
  } as const;

  return (
    <div className={`${styles.page} app-narrow-block`}>
      <div className={styles.row}>
        <div style={{ flex: 1 }}>
          <div className={styles.label}>{t("settings.username_role_label")}</div>
          <div className={styles.value}>
            <strong>{username || "—"}</strong>
            {" · "}
            {isAdminRole(role) ? t("settings.role_admin") : t("settings.role_user")}
          </div>
        </div>
      </div>

      {edit !== "avatar" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>{t("settings.avatar.label")}</div>
            <div className={styles.avatarCircle}>
              {avatarUrl ? (
                <img src={avatarUrl} alt="" />
              ) : (
                <div
                  style={{
                    width: "100%",
                    height: "100%",
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    fontSize: 28,
                    color: "#bbb",
                  }}
                >
                  {(username || "?").slice(0, 1).toUpperCase()}
                </div>
              )}
            </div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("avatar")}>
            {t("common.edit")}
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>{t("settings.avatar.title")}</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </div>
          <div className={styles.avatarBody}>
            <div>
              <div className={styles.cropWrap}>
                {imgSrc ? (
                  <Cropper
                    image={imgSrc}
                    crop={crop}
                    zoom={zoom}
                    aspect={1}
                    cropShape="round"
                    showGrid={false}
                    onCropChange={setCrop}
                    onZoomChange={setZoom}
                    onCropComplete={(_c, px) => setCroppedAreaPixels(px)}
                  />
                ) : (
                  <div style={{ height: "100%", display: "flex", alignItems: "center", justifyContent: "center" }}>
                    <span style={{ color: "#666", fontSize: 13 }}>{t("settings.avatar.no_photo")}</span>
                  </div>
                )}
              </div>
              <div className={styles.slider}>
                <span style={{ color: "#888", fontSize: 12 }}>−</span>
                <input
                  type="range"
                  min={1}
                  max={3}
                  step={0.05}
                  value={zoom}
                  onChange={(e) => setZoom(Number(e.target.value))}
                />
                <span style={{ color: "#888", fontSize: 12 }}>+</span>
              </div>
            </div>
            <div className={styles.avatarSide}>
              <input ref={fileRef} type="file" accept="image/*" style={{ display: "none" }} onChange={onPickFile} />
              <button type="button" className={styles.selectPhoto} onClick={() => fileRef.current?.click()}>
                {t("settings.avatar.select_photo")}
              </button>
              <p className={styles.hint}>
                {t("settings.avatar.hint_part1")}
                <button type="button" className={styles.link} onClick={() => void removeAvatar()}>
                  {t("settings.avatar.hint_part2")}
                </button>
              </p>
            </div>
          </div>
          <div className={styles.saveRow}>
            <button
              type="button"
              className={canSaveAvatar ? `${styles.saveBtn} ${styles.saveBtnActive}` : styles.saveBtn}
              disabled={!canSaveAvatar || loading}
              onClick={() => void saveAvatar()}
            >
              {t("settings.avatar.save")}
            </button>
          </div>
        </div>
      )}

      {edit !== "password" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>{t("settings.password.label")}</div>
            <div className={styles.value}>{t("settings.password.value")}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("password")}>
            {t("common.edit")}
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>{t("settings.password.title")}</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            {t("settings.password.hint")}
          </p>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>{t("settings.password.new_password")}</div>
              <input
                className={styles.darkField}
                type="password"
                autoComplete="new-password"
                value={pw1}
                onChange={(e) => setPw1(e.target.value)}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.password.confirm_password")}</div>
              <input
                className={styles.darkField}
                type="password"
                autoComplete="new-password"
                value={pw2}
                onChange={(e) => setPw2(e.target.value)}
              />
            </div>
          </div>
          <div className={styles.saveRow}>
            <button
              type="button"
              className={`${styles.saveBtn} ${styles.saveBtnActive}`}
              disabled={loading || pw1.length < 6 || pw1 !== pw2}
              onClick={() => void savePassword()}
            >
              {t("settings.password.save")}
            </button>
          </div>
        </div>
      )}

      <div className={styles.sectionBar}>{t("settings.page_section")}</div>

      {edit !== "language" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>{t("settings.language.label")}</div>
            <div className={styles.value}>{uiLocaleLabel(loc)}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("language")}>
            {t("common.edit")}
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>{t("settings.language.title")}</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </div>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>{t("settings.language.field_label")}</div>
              <Select
                style={selectDark}
                value={langDraft}
                options={UI_LOCALES}
                onChange={(v) => setLangDraft(v)}
                placeholder={t("settings.language.placeholder")}
                popupMatchSelectWidth={false}
              />
            </div>
          </div>
          <div className={styles.saveRow}>
            <button
              type="button"
              className={`${styles.saveBtn} ${styles.saveBtnActive}`}
              disabled={loading}
              onClick={() => void saveLanguage()}
            >
              {t("settings.language.save")}
            </button>
          </div>
        </div>
      )}

      {edit !== "audio" ? (
        <div className={styles.row}>
          <div style={{ flex: 1 }}>
            <div className={styles.label}>{t("settings.audio.label")}</div>
            <div className={styles.value}>{summarizePlayerPrefsLocalized(prefs, t)}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("audio")}>
            {t("common.edit")}
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>{t("settings.audio.title")}</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            {t("settings.audio.hint")}
          </p>
          <div className={styles.formStack}>
            <Checkbox
              className={styles.checkbox}
              checked={audioDraft.auto_select}
              onChange={(e) => setAudioDraft({ ...audioDraft, auto_select: e.target.checked })}
            >
              {t("settings.audio.auto_select")}
            </Checkbox>
            <div>
              <div className={styles.fieldLabel}>{t("settings.audio.preferred_audio_lang")}</div>
              <Select
                style={selectDark}
                value={audioDraft.preferred_audio_lang || ""}
                options={LANG_TRACKS}
                onChange={(v) => setAudioDraft({ ...audioDraft, preferred_audio_lang: v })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.audio.preferred_subtitle_lang")}</div>
              <Select
                style={selectDark}
                value={audioDraft.preferred_subtitle_lang || ""}
                options={LANG_TRACKS}
                onChange={(v) => setAudioDraft({ ...audioDraft, preferred_subtitle_lang: v })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.audio.subtitle_mode")}</div>
              <Select
                style={selectDark}
                value={audioDraft.subtitle_mode}
                options={SUBTITLE_MODE_OPTS}
                onChange={(v) => setAudioDraft({ ...audioDraft, subtitle_mode: v as PlayerPrefs["subtitle_mode"] })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.audio.sdh_search")}</div>
              <Select
                style={selectDark}
                value={audioDraft.sdh_search}
                options={SDH_OPTS}
                onChange={(v) => setAudioDraft({ ...audioDraft, sdh_search: v as PlayerPrefs["sdh_search"] })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.audio.forced_search")}</div>
              <Select
                style={selectDark}
                value={audioDraft.forced_search}
                options={FORCED_OPTS}
                onChange={(v) => setAudioDraft({ ...audioDraft, forced_search: v as PlayerPrefs["forced_search"] })}
                disabled={!audioDraft.auto_select}
              />
            </div>
          </div>
          <div className={styles.saveRow}>
            <button
              type="button"
              className={`${styles.saveBtn} ${styles.saveBtnActive}`}
              disabled={loading}
              onClick={() => void saveAudio()}
            >
              {t("settings.audio.save")}
            </button>
          </div>
        </div>
      )}

      {edit !== "subtitle" ? (
        <div className={styles.row}>
          <div style={{ flex: 1 }}>
            <div className={styles.label}>{t("settings.subtitle_appearance.label")}</div>
            <div className={styles.value}>{subtitleAppearanceSummary}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("subtitle")}>
            {t("common.edit")}
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>{t("settings.subtitle_appearance.title")}</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              {t("common.cancel")}
            </button>
          </div>

          <div className={styles.previewFrame}>
            <div className={styles.previewInner}>
              <span style={previewSubtitleBoxStyle(subtitleDraft)}>{t("settings.subtitle_appearance.preview_text")}</span>
            </div>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            {t("settings.subtitle_appearance.hint")}
          </p>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.text_size")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.text_size}
                options={subtitleTextSizeOptions}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, text_size: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.text_color")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.text_color}
                options={subtitleTextColorOptions}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, text_color: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.shadow")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.shadow}
                options={subtitleShadowOptions}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, shadow: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.bg_color")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.bg_color}
                options={subtitleBgColorOptions}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, bg_color: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.bg_opacity")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.bg_opacity}
                options={BG_OPACITY_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, bg_opacity: v })}
                disabled={subtitleDraft.bg_color === "transparent"}
              />
            </div>
          </div>

          <h3 className={styles.sectionHeading} style={{ marginTop: 20 }}>
            {t("settings.subtitle_appearance.section_position")}
          </h3>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.pos_bottom")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.pos_bottom}
                options={POS_PCT_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, pos_bottom: v })}
              />
              <p className={styles.hint} style={{ marginTop: 6 }}>
                {t("settings.subtitle_appearance.pos_bottom_hint")}
              </p>
            </div>
            <div>
              <div className={styles.fieldLabel}>{t("settings.subtitle_appearance.pos_top")}</div>
              <Select
                style={selectDark}
                value={subtitleDraft.pos_top}
                options={POS_PCT_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, pos_top: v })}
              />
              <p className={styles.hint} style={{ marginTop: 6 }}>
                {t("settings.subtitle_appearance.pos_top_hint")}
              </p>
            </div>
          </div>
          <div className={styles.saveRow}>
            <button
              type="button"
              className={`${styles.saveBtn} ${styles.saveBtnActive}`}
              disabled={loading}
              onClick={() => void saveSubtitleAppearance()}
            >
              {t("settings.subtitle_appearance.save")}
            </button>
          </div>
        </div>
      )}

      <p className={styles.footnote}>
        {isAdminRole(role) ? t("settings.footnote_admin") : t("settings.footnote_user")}{" "}
        {t("settings.footnote_warning")}
      </p>
    </div>
  );
}
