import { Checkbox, Select, message } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";
import Cropper, { type Area } from "react-easy-crop";
import "react-easy-crop/react-easy-crop.css";

import {
  changeUserPassword,
  deleteUserAvatar,
  fetchUserInfo,
  updateUserProfile,
  uploadUserAvatar,
} from "../api/client";
import { defaultPlayerPrefs, normalizePlayerPrefs, summarizePlayerPrefs, type PlayerPrefs } from "../lib/playerPrefs";
import { isAdminRole, useAuthStore } from "../store/auth";
import { getCroppedCircularPngBlob } from "../utils/cropImage";
import {
  BG_COLOR_OPTIONS,
  BG_OPACITY_OPTIONS,
  POS_PCT_OPTIONS,
  SHADOW_OPTIONS,
  TEXT_COLOR_OPTIONS,
  TEXT_SIZE_OPTIONS,
  normalizeSubtitleAppearance,
  previewSubtitleBoxStyle,
  summarizeSubtitleAppearance,
  type SubtitleAppearance,
} from "../lib/subtitleAppearance";
import styles from "./Settings.module.css";

type EditMode = null | "avatar" | "language" | "audio" | "password" | "subtitle";

const UI_LOCALES: { value: string; label: string }[] = [
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
  { value: "ja", label: "日本語" },
  { value: "ko", label: "한국어" },
  { value: "fr", label: "Français" },
  { value: "de", label: "Deutsch" },
];

const LANG_TRACKS: { value: string; label: string }[] = [
  { value: "", label: "选择语言" },
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
  { value: "ja", label: "日本語" },
  { value: "ko", label: "한국어" },
  { value: "fr", label: "Français" },
  { value: "de", label: "Deutsch" },
  { value: "es", label: "Español" },
  { value: "ru", label: "Русский" },
];

const SUBTITLE_MODE_OPTS: { value: PlayerPrefs["subtitle_mode"]; label: string }[] = [
  { value: "foreign", label: "以外语音频显示" },
  { value: "always", label: "始终显示" },
  { value: "off", label: "关闭" },
];

const SDH_OPTS: { value: PlayerPrefs["sdh_search"]; label: string }[] = [
  { value: "prefer_non_sdh", label: "首选非SDH字幕" },
  { value: "prefer_sdh", label: "首选SDH字幕" },
];

const FORCED_OPTS: { value: PlayerPrefs["forced_search"]; label: string }[] = [
  { value: "prefer_non_forced", label: "非强制字幕优先" },
  { value: "prefer_forced", label: "强制字幕优先" },
];

function uiLocaleLabel(code: string | null | undefined): string {
  const c = (code || "zh").toLowerCase();
  return UI_LOCALES.find((x) => x.value === c)?.label || code || "中文";
}

export default function SettingsPage() {
  const username = useAuthStore((s) => s.username);
  const role = useAuthStore((s) => s.role);
  const avatarUrl = useAuthStore((s) => s.avatarUrl);
  const uiLocale = useAuthStore((s) => s.uiLocale);
  const playerPrefs = useAuthStore((s) => s.playerPrefs);
  const setProfile = useAuthStore((s) => s.setProfile);

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
      message.error("无法加载账号信息");
    }
  }, [setProfile]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const prefs = playerPrefs ?? defaultPlayerPrefs();
  const loc = uiLocale || "zh";

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
      message.warning("请选择图片文件");
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
      message.success("头像已更新");
      setEdit(null);
    } catch {
      message.error("上传失败");
    } finally {
      setLoading(false);
    }
  };

  const removeAvatar = async () => {
    setLoading(true);
    try {
      await deleteUserAvatar();
      await refresh();
      message.success("已删除头像");
      setEdit(null);
    } catch {
      message.error("删除失败");
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
      message.success("语言已保存");
      setEdit(null);
    } catch {
      message.error("保存失败");
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
      message.success("音频与字幕设置已保存");
      setEdit(null);
    } catch {
      message.error("保存失败");
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
      message.success("字幕外观已保存");
      setEdit(null);
    } catch {
      message.error("保存失败");
    } finally {
      setLoading(false);
    }
  };

  const savePassword = async () => {
    if (pw1.length < 6) {
      message.warning("密码至少 6 位");
      return;
    }
    if (pw1 !== pw2) {
      message.warning("两次输入的密码不一致");
      return;
    }
    setLoading(true);
    try {
      await changeUserPassword(pw1, pw2);
      message.success("密码已更新");
      setPw1("");
      setPw2("");
      setEdit(null);
    } catch {
      message.error("修改密码失败");
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
          <div className={styles.label}>用户名 / 角色</div>
          <div className={styles.value}>
            <strong>{username || "—"}</strong>
            {" · "}
            {isAdminRole(role) ? "管理员" : "普通用户"}
          </div>
        </div>
      </div>

      {edit !== "avatar" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>头像</div>
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
            编辑
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>头像</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              取消
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
                    <span style={{ color: "#666", fontSize: 13 }}>请选择相片</span>
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
                选择相片
              </button>
              <p className={styles.hint}>
                您可以使用下方的滑块调整照片的比例，并通过在圆圈内拖动来调整裁剪。或者您可以{" "}
                <button type="button" className={styles.link} onClick={() => void removeAvatar()}>
                  删除您的个人资料图片。
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
              保存修改
            </button>
          </div>
        </div>
      )}

      {edit !== "password" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>密码</div>
            <div className={styles.value}>已设置登录密码</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("password")}>
            编辑
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>密码</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              取消
            </button>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            设置新登录密码（至少 6 位）。保存后请使用新密码登录。
          </p>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>新密码</div>
              <input
                className={styles.darkField}
                type="password"
                autoComplete="new-password"
                value={pw1}
                onChange={(e) => setPw1(e.target.value)}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>新密码确认</div>
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
              保存修改
            </button>
          </div>
        </div>
      )}

      <div className={styles.sectionBar}>设置</div>

      {edit !== "language" ? (
        <div className={styles.row}>
          <div>
            <div className={styles.label}>语言</div>
            <div className={styles.value}>{uiLocaleLabel(loc)}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("language")}>
            编辑
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>语言</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              取消
            </button>
          </div>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>界面语言（将同步首选音轨 / 字幕语言）</div>
              <Select
                style={selectDark}
                value={langDraft}
                options={UI_LOCALES}
                onChange={(v) => setLangDraft(v)}
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
              保存修改
            </button>
          </div>
        </div>
      )}

      {edit !== "audio" ? (
        <div className={styles.row}>
          <div style={{ flex: 1 }}>
            <div className={styles.label}>音频和字幕设置</div>
            <div className={styles.value}>{summarizePlayerPrefs(prefs)}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("audio")}>
            编辑
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>音频和字幕设置</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              取消
            </button>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            这些设置决定了在播放器中如何选择音频和字幕轨。
          </p>
          <div className={styles.formStack}>
            <Checkbox
              className={styles.checkbox}
              checked={audioDraft.auto_select}
              onChange={(e) => setAudioDraft({ ...audioDraft, auto_select: e.target.checked })}
            >
              自动选择音频及字幕曲目
            </Checkbox>
            <div>
              <div className={styles.fieldLabel}>首选音频语言</div>
              <Select
                style={selectDark}
                value={audioDraft.preferred_audio_lang || ""}
                options={LANG_TRACKS}
                onChange={(v) => setAudioDraft({ ...audioDraft, preferred_audio_lang: v })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>首选字幕语言</div>
              <Select
                style={selectDark}
                value={audioDraft.preferred_subtitle_lang || ""}
                options={LANG_TRACKS}
                onChange={(v) => setAudioDraft({ ...audioDraft, preferred_subtitle_lang: v })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>自动选择字幕模式</div>
              <Select
                style={selectDark}
                value={audioDraft.subtitle_mode}
                options={SUBTITLE_MODE_OPTS}
                onChange={(v) => setAudioDraft({ ...audioDraft, subtitle_mode: v as PlayerPrefs["subtitle_mode"] })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>适用于聋哑或听力障碍人士的字幕 (SDH) 搜索</div>
              <Select
                style={selectDark}
                value={audioDraft.sdh_search}
                options={SDH_OPTS}
                onChange={(v) => setAudioDraft({ ...audioDraft, sdh_search: v as PlayerPrefs["sdh_search"] })}
                disabled={!audioDraft.auto_select}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>强制字幕搜索</div>
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
              保存修改
            </button>
          </div>
        </div>
      )}

      {edit !== "subtitle" ? (
        <div className={styles.row}>
          <div style={{ flex: 1 }}>
            <div className={styles.label}>字幕外观</div>
            <div className={styles.value}>{summarizeSubtitleAppearance(prefs.subtitle_appearance)}</div>
          </div>
          <button type="button" className={styles.edit} onClick={() => setEdit("subtitle")}>
            编辑
          </button>
        </div>
      ) : (
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <span className={styles.panelTitle}>字幕外观</span>
            <button type="button" className={styles.cancel} onClick={() => setEdit(null)}>
              取消
            </button>
          </div>

          <div className={styles.previewFrame}>
            <div className={styles.previewInner}>
              <span style={previewSubtitleBoxStyle(subtitleDraft)}>这些设置会影响此设备上的字幕</span>
            </div>
          </div>
          <p className={styles.hint} style={{ marginBottom: 16 }}>
            这些设置不适用于图形字幕（PGS、DVD 等）以及有其自己内嵌样式的字幕（ASS/SSA）。
          </p>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>文本大小</div>
              <Select
                style={selectDark}
                value={subtitleDraft.text_size}
                options={TEXT_SIZE_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, text_size: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>文本颜色</div>
              <Select
                style={selectDark}
                value={subtitleDraft.text_color}
                options={TEXT_COLOR_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, text_color: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>投影</div>
              <Select
                style={selectDark}
                value={subtitleDraft.shadow}
                options={SHADOW_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, shadow: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>背景色</div>
              <Select
                style={selectDark}
                value={subtitleDraft.bg_color}
                options={BG_COLOR_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, bg_color: v })}
              />
            </div>
            <div>
              <div className={styles.fieldLabel}>背景不透明度</div>
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
            字幕位置
          </h3>
          <div className={styles.formStack}>
            <div>
              <div className={styles.fieldLabel}>底部边缘位置</div>
              <Select
                style={selectDark}
                value={subtitleDraft.pos_bottom}
                options={POS_PCT_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, pos_bottom: v })}
              />
              <p className={styles.hint} style={{ marginTop: 6 }}>
                设置字幕相对于屏幕底部的垂直位置。
              </p>
            </div>
            <div>
              <div className={styles.fieldLabel}>顶部边缘位置</div>
              <Select
                style={selectDark}
                value={subtitleDraft.pos_top}
                options={POS_PCT_OPTIONS}
                onChange={(v) => setSubtitleDraft({ ...subtitleDraft, pos_top: v })}
              />
              <p className={styles.hint} style={{ marginTop: 6 }}>
                设置字幕相对于屏幕顶部允许的最高垂直位置。当字幕包含要放置在顶部的定位指令时使用此功能。
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
              保存修改
            </button>
          </div>
        </div>
      )}

      <p className={styles.footnote}>
        {isAdminRole(role)
          ? "管理员可使用侧边栏「媒体库」「上传」「控制台」进行管理与运维。"
          : "您可「浏览媒体」「我的收藏」与播放；媒体库与上传由管理员维护。"}{" "}
        生产环境请在服务器 config.yml 中修改 JWT 密钥；勿共享管理员账号。
      </p>
    </div>
  );
}
