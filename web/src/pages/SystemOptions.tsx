import {
  Button,
  Card,
  Divider,
  Flex,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Tabs,
  Typography,
  message,
} from "antd";
import { SearchOutlined, ApiOutlined, CloudDownloadOutlined } from "@ant-design/icons";
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  fetchSystemOptions,
  fetchUserInfo,
  saveSystemOptions,
  testSystemOptionsASR,
  testSystemOptionsOCR,
  installSystemOptionsASR,
  installSystemOptionsOCR,
  testSystemOptionsPhotoClassify,
  installSystemOptionsPhotoClassify,
  testSystemOptionsPhotoFace,
  installSystemOptionsPhotoFace,
  testSystemOptionsDocTrans,
  installSystemOptionsDocTrans,
  installLibreOfficeDocTrans,
  updateUserProfile,
  type SystemOptions,
  type SystemOptionsResponse,
  type RecognitionTestResult,
  type DocTransTestResult,
} from "../api/client";
import { languageOptions, resolveLocale, useT, type TranslateFn } from "../i18n";
import { defaultPlayerPrefs, normalizePlayerPrefs } from "../lib/playerPrefs";
import { useAuthStore } from "../store/auth";

function defaultSystemOptions(): SystemOptions {
  return {
    general: {
      display_language: "zh-CN",
      start_on_boot: false,
      open_browser_on_first_start: true,
      maintenance_mode: false,
      cache_path: "",
      auto_update_enabled: false,
    },
    playback: {
      home_stream_quality: "auto",
      screen_orientation: "auto",
    },
    transcoder: {
      quality: "auto",
      temp_dir: "",
      download_temp_dir: "",
      throttle_buffer_seconds: 60,
      background_x264_preset: "veryfast",
      hardware_acceleration: "none",
      enable_hardware_encoding: false,
      disable_video_stream_transcoding: false,
      max_cpu_concurrent: "unlimited",
      max_background_concurrent: "1",
    },
    recognition: {
      asr: {
        auto_on_scan: true,
        provider: "none",
        whisper_path: "whisper",
        extra_args: [],
        shell: "",
      },
      ocr: {
        enabled: false,
        tesseract_path: "tesseract",
        tessdata_prefix: "",
        languages: "chi_sim+eng",
        python_path: "",
        script_path: "tools/subtitle_ocr/bitmap_subtitle_ocr.py",
        pgsrip_path: "",
        mkvextract_path: "",
        mkvmerge_path: "",
      },
      ai_proofread: true,
    },
    photo_classify: {
      auto_on_scan: true,
      engine: "auto",
      python_path: "",
      script_path: "tools/photo_classify/classify.py",
      model_path: "tools/photo_classify/models/mobilenetv2-7.onnx",
      labels_path: "tools/photo_classify/imagenet_labels.txt",
    },
    photo_face: {
      auto_on_scan: true,
      python_path: "",
      script_path: "tools/photo_face/detect.py",
      similarity_threshold: 0.45,
    },
    doc_trans: {
      enabled: true,
      engine_order: ["office", "wps", "libreoffice"],
      libreoffice_path: "tools/doctran/LibreOffice/program/soffice.exe",
      soffice_path: "tools/doctran/LibreOffice/program/soffice.exe",
      office_path: "",
      wps_path: "",
      cache_dir: "",
      cache_ttl_days: 30,
      timeout_seconds: 180,
    },
  };
}

/** Merge API payload with defaults so partial/null fields never crash the form. */
function mergeSystemOptions(data: Partial<SystemOptions> | null | undefined): SystemOptions {
  const base = defaultSystemOptions();
  if (!data) return base;
  const asr = { ...base.recognition.asr, ...(data.recognition?.asr ?? {}) };
  const extraRaw = data.recognition?.asr?.extra_args;
  asr.extra_args = Array.isArray(extraRaw) ? extraRaw : base.recognition.asr.extra_args;
  return {
    general: { ...base.general, ...(data.general ?? {}) },
    playback: { ...base.playback, ...(data.playback ?? {}) },
    transcoder: { ...base.transcoder, ...(data.transcoder ?? {}) },
    recognition: {
      asr,
      ocr: { ...base.recognition.ocr, ...(data.recognition?.ocr ?? {}) },
      ai_proofread:
        typeof data.recognition?.ai_proofread === "boolean"
          ? data.recognition.ai_proofread
          : base.recognition.ai_proofread,
    },
    photo_classify: { ...base.photo_classify, ...(data.photo_classify ?? {}) },
    photo_face: { ...base.photo_face, ...(data.photo_face ?? {}) },
    doc_trans: { ...base.doc_trans, ...(data.doc_trans ?? {}) },
  };
}

function buildHomeStreamQualityOptions(t: TranslateFn): { value: string; label: string }[] {
  const bitrate = (resolution: string, mbps: number | string) =>
    t("system_options.common.stream_bitrate", { resolution, mbps });
  const out: { value: string; label: string }[] = [
    { value: "auto", label: t("system_options.common.auto") },
  ];
  for (const m of [200, 160, 140, 120, 100, 80, 60, 40]) {
    out.push({ value: `4k-${m}mbps`, label: bitrate("4K", m) });
  }
  for (const m of [60, 50, 40, 30, 25, 20, 15, 12, 10, 8, 6, 5]) {
    out.push({ value: `1080p-${m}mbps`, label: bitrate("1080p", m) });
  }
  for (const m of [8, 6, 4, 3, 2]) {
    out.push({ value: `720p-${m}mbps`, label: bitrate("720p", m) });
  }
  for (const m of [4, 3, 2]) {
    out.push({ value: `480p-${m}mbps`, label: bitrate("480p", m) });
  }
  out.push({ value: "480p-1_5mbps", label: bitrate("480p", "1.5") });
  return out;
}

/**
 * Build dropdown options for the admin's preferred display language. The
 * codes match the per-user `ui_locale` field so the admin's selection is
 * persisted directly on their account (per FR-ADM-05).
 */
function buildDisplayLanguageOptions(): { value: string; label: string }[] {
  return languageOptions();
}

function buildTranscoderQualityOptions(t: TranslateFn): { value: string; label: string }[] {
  return [
    { value: "auto", label: t("system_options.common.auto") },
    { value: "max", label: t("system_options.common.quality_max") },
    { value: "high", label: t("system_options.common.quality_high") },
    { value: "medium", label: t("system_options.common.quality_medium") },
    { value: "low", label: t("system_options.common.quality_low") },
  ];
}

const HW_ACCEL_VALUES = ["none", "amf", "nvenc", "qsv", "vaapi"] as const;

function buildHardwareAccelerationOptions(
  t: TranslateFn,
  available: readonly string[],
): { value: string; label: string }[] {
  const availableSet = new Set(available);
  return HW_ACCEL_VALUES.filter((value) => value === "none" || availableSet.has(value)).map((value) => ({
    value,
    label: t(`system_options.transcoder.hw_accel.${value}`),
  }));
}

function buildX264PresetOptions(t: TranslateFn): { value: string; label: string }[] {
  const presets = [
    "ultrafast",
    "superfast",
    "veryfast",
    "faster",
    "fast",
    "medium",
    "slow",
    "slower",
    "veryslow",
  ] as const;
  return presets.map((value) => ({
    value,
    label: t(`system_options.transcoder.x264.${value}`),
  }));
}

function buildCpuConcurrentOptions(t: TranslateFn): { value: string; label: string }[] {
  return [
    { value: "unlimited", label: t("system_options.common.unlimited") },
    ...Array.from({ length: 16 }, (_, i) => ({
      value: String(i + 1),
      label: String(i + 1),
    })),
  ];
}

const BG_CONCURRENT_OPTIONS = Array.from({ length: 8 }, (_, i) => ({
  value: String(i + 1),
  label: String(i + 1),
}));

/** compact: 控件随内容变宽（有上限）；full: 路径类输入在可用宽度内拉满（有上限） */
type SettingControlLayout = "compact" | "full";

function SettingRow(props: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
  controlLayout?: SettingControlLayout;
}) {
  const { title, description, children, controlLayout = "compact" } = props;

  const controlWrapStyle: CSSProperties =
    controlLayout === "full"
      ? { width: "min(100%, 720px)" }
      : {
          display: "inline-block",
          width: "fit-content",
          maxWidth: "min(100%, 560px)",
        };

  return (
    <Flex vertical gap={8} style={{ width: "100%" }}>
      <Typography.Text strong>{title}</Typography.Text>
      <div style={controlWrapStyle}>{children}</div>
      {description ? (
        <div>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {description}
          </Typography.Text>
        </div>
      ) : null}
    </Flex>
  );
}

function buildAsrProviderOptions(t: TranslateFn): { value: string; label: string }[] {
  return [
    { value: "none", label: t("system_options.asr.provider_none") },
    { value: "whisper_cli", label: t("system_options.asr.provider_whisper_cli") },
    { value: "shell", label: t("system_options.asr.provider_shell") },
  ];
}

function buildPhotoClassifyEngineOptions(t: TranslateFn): { value: string; label: string }[] {
  return [
    { value: "auto", label: t("system_options.photo_classify.engine_auto") },
    { value: "heuristic", label: t("system_options.photo_classify.engine_heuristic") },
    { value: "onnx", label: t("system_options.photo_classify.engine_onnx") },
  ];
}

export default function SystemOptionsPage() {
  const t = useT();
  const uiLocale = useAuthStore((s) => s.uiLocale);
  const setProfile = useAuthStore((s) => s.setProfile);
  const adminLanguage = useMemo(() => resolveLocale(uiLocale), [uiLocale]);
  const [languageSaving, setLanguageSaving] = useState(false);
  const DISPLAY_LANGUAGE_OPTIONS = useMemo(() => buildDisplayLanguageOptions(), []);
  const homeStreamQualityOptions = useMemo(() => buildHomeStreamQualityOptions(t), [t]);
  const transcoderQualityOptions = useMemo(() => buildTranscoderQualityOptions(t), [t]);
  const x264PresetOptions = useMemo(() => buildX264PresetOptions(t), [t]);
  const [availableHwAccel, setAvailableHwAccel] = useState<string[]>([]);
  const hardwareAccelerationOptions = useMemo(
    () => buildHardwareAccelerationOptions(t, availableHwAccel),
    [t, availableHwAccel],
  );
  const cpuConcurrentOptions = useMemo(() => buildCpuConcurrentOptions(t), [t]);
  const asrProviderOptions = useMemo(() => buildAsrProviderOptions(t), [t]);
  const photoClassifyEngineOptions = useMemo(() => buildPhotoClassifyEngineOptions(t), [t]);
  const screenOrientationOptions = useMemo(
    () => [
      { value: "auto", label: t("system_options.playback.orientation_auto") },
      { value: "lock_landscape", label: t("system_options.playback.orientation_lock_landscape") },
      { value: "device", label: t("system_options.playback.orientation_device") },
    ],
    [t],
  );

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [opts, setOpts] = useState<SystemOptions>(() => defaultSystemOptions());
  const [baseline, setBaseline] = useState<SystemOptions>(() => defaultSystemOptions());
  const [asrTesting, setAsrTesting] = useState(false);
  const [ocrTesting, setOcrTesting] = useState(false);
  const [asrInstalling, setAsrInstalling] = useState(false);
  const [ocrInstalling, setOcrInstalling] = useState(false);
  const [asrTestResult, setAsrTestResult] = useState<RecognitionTestResult | null>(null);
  const [ocrTestResult, setOcrTestResult] = useState<RecognitionTestResult | null>(null);
  const [classifyTesting, setClassifyTesting] = useState(false);
  const [classifyInstalling, setClassifyInstalling] = useState(false);
  const [classifyTestResult, setClassifyTestResult] = useState<RecognitionTestResult | null>(null);
  const [faceTesting, setFaceTesting] = useState(false);
  const [faceInstalling, setFaceInstalling] = useState(false);
  const [faceTestResult, setFaceTestResult] = useState<RecognitionTestResult | null>(null);
  const [docTransTesting, setDocTransTesting] = useState(false);
  const [docTransInstalling, setDocTransInstalling] = useState(false);
  const [docTransInstallingLO, setDocTransInstallingLO] = useState(false);
  const [docTransTestResult, setDocTransTestResult] = useState<DocTransTestResult | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data: SystemOptionsResponse = await fetchSystemOptions();
      setAvailableHwAccel(data.available_hardware_acceleration ?? []);
      const merged = mergeSystemOptions(data);
      setOpts(merged);
      setBaseline(merged);
      setAsrTestResult(null);
      setOcrTestResult(null);
      setClassifyTestResult(null);
    } catch {
      message.error(t("system_options.messages.load_failed"));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const dirty = useMemo(() => JSON.stringify(opts) !== JSON.stringify(baseline), [opts, baseline]);

  const save = async () => {
    setSaving(true);
    try {
      const saved = await saveSystemOptions(opts);
      const merged = mergeSystemOptions(saved);
      setOpts(merged);
      setBaseline(merged);
      message.success(t("system_options.messages.saved"));
    } catch {
      message.error(t("system_options.messages.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const handleAdminLanguageChange = async (next: string) => {
    if (next === adminLanguage) return;
    setLanguageSaving(true);
    try {
      const data = await updateUserProfile({ ui_locale: next });
      const u = await fetchUserInfo();
      setProfile(u.username, u.role, {
        canPlay: u.can_play !== false,
        avatarUrl: u.avatar_url || null,
        uiLocale: data.ui_locale,
        playerPrefs: data.player_prefs
          ? normalizePlayerPrefs(data.player_prefs)
          : u.player_prefs
            ? normalizePlayerPrefs(u.player_prefs)
            : defaultPlayerPrefs(),
      });
      message.success(t("system_options.general.saved"));
    } catch {
      message.error(t("system_options.general.save_failed"));
    } finally {
      setLanguageSaving(false);
    }
  };

  const reset = () => {
    setOpts(baseline);
    setAsrTestResult(null);
    setOcrTestResult(null);
    setClassifyTestResult(null);
    message.info(t("system_options.messages.reset_to_last_loaded"));
  };

  const runAsrTest = async () => {
    setAsrTesting(true);
    setAsrTestResult(null);
    try {
      const result = await testSystemOptionsASR(opts.recognition.asr);
      setAsrTestResult(result);
    } catch {
      setAsrTestResult({ ok: false, message: t("system_options.messages.test_request_failed") });
    } finally {
      setAsrTesting(false);
    }
  };

  const runOcrTest = async () => {
    setOcrTesting(true);
    setOcrTestResult(null);
    try {
      const result = await testSystemOptionsOCR(opts.recognition.ocr);
      setOcrTestResult(result);
    } catch {
      setOcrTestResult({ ok: false, message: t("system_options.messages.test_request_failed") });
    } finally {
      setOcrTesting(false);
    }
  };

  const applyInstalledRecognition = (recognition: Partial<SystemOptions["recognition"]> | undefined) => {
    if (!recognition) return;
    const patch = mergeSystemOptions({ recognition: recognition as SystemOptions["recognition"] });
    setOpts((p) => ({ ...p, recognition: patch.recognition }));
    setBaseline((p) => ({ ...p, recognition: patch.recognition }));
  };

  const runAsrInstall = async () => {
    setAsrInstalling(true);
    setAsrTestResult(null);
    try {
      const result = await installSystemOptionsASR();
      if (result.recognition) {
        applyInstalledRecognition(result.recognition);
      }
      setAsrTestResult({ ok: result.ok, message: result.message });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.error(result.message);
      }
    } catch {
      message.error(t("system_options.messages.asr_install_request_failed"));
    } finally {
      setAsrInstalling(false);
    }
  };

  const runOcrInstall = async () => {
    setOcrInstalling(true);
    setOcrTestResult(null);
    try {
      const result = await installSystemOptionsOCR();
      if (result.recognition) {
        applyInstalledRecognition(result.recognition);
      }
      setOcrTestResult({ ok: result.ok, message: result.message });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.error(result.message);
      }
    } catch {
      message.error(t("system_options.messages.ocr_install_request_failed"));
    } finally {
      setOcrInstalling(false);
    }
  };

  const applyInstalledPhotoClassify = (photoClassify: SystemOptions["photo_classify"] | undefined) => {
    if (!photoClassify) return;
    const patch = mergeSystemOptions({ photo_classify: photoClassify });
    setOpts((p) => ({ ...p, photo_classify: patch.photo_classify }));
    setBaseline((p) => ({ ...p, photo_classify: patch.photo_classify }));
  };

  const runClassifyTest = async () => {
    setClassifyTesting(true);
    setClassifyTestResult(null);
    try {
      const result = await testSystemOptionsPhotoClassify(opts.photo_classify);
      setClassifyTestResult(result);
    } catch {
      setClassifyTestResult({ ok: false, message: t("system_options.messages.test_request_failed") });
    } finally {
      setClassifyTesting(false);
    }
  };

  const runClassifyInstall = async () => {
    setClassifyInstalling(true);
    setClassifyTestResult(null);
    try {
      const result = await installSystemOptionsPhotoClassify();
      if (result.photo_classify) {
        applyInstalledPhotoClassify(result.photo_classify);
      }
      setClassifyTestResult({ ok: result.ok, message: result.message });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.error(result.message);
      }
    } catch {
      message.error(t("system_options.messages.classify_install_request_failed"));
    } finally {
      setClassifyInstalling(false);
    }
  };

  const applyInstalledPhotoFace = (photoFace: SystemOptions["photo_face"] | undefined) => {
    if (!photoFace) return;
    const patch = mergeSystemOptions({ photo_face: photoFace });
    setOpts((p) => ({ ...p, photo_face: patch.photo_face }));
    setBaseline((p) => ({ ...p, photo_face: patch.photo_face }));
  };

  const runFaceTest = async () => {
    setFaceTesting(true);
    setFaceTestResult(null);
    try {
      const result = await testSystemOptionsPhotoFace(opts.photo_face);
      setFaceTestResult(result);
    } catch {
      setFaceTestResult({ ok: false, message: t("system_options.messages.test_request_failed") });
    } finally {
      setFaceTesting(false);
    }
  };

  const runFaceInstall = async () => {
    setFaceInstalling(true);
    setFaceTestResult(null);
    try {
      const result = await installSystemOptionsPhotoFace();
      if (result.photo_face) {
        applyInstalledPhotoFace(result.photo_face);
      }
      setFaceTestResult({ ok: result.ok, message: result.message });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.error(result.message);
      }
    } catch {
      message.error(t("system_options.messages.face_install_request_failed"));
    } finally {
      setFaceInstalling(false);
    }
  };

  const applyInstalledDocTrans = (docTrans: SystemOptions["doc_trans"] | undefined) => {
    if (!docTrans) return;
    const patch = mergeSystemOptions({ doc_trans: docTrans });
    setOpts((p) => ({ ...p, doc_trans: patch.doc_trans }));
    setBaseline((p) => ({ ...p, doc_trans: patch.doc_trans }));
  };

  const runDocTransTest = async () => {
    setDocTransTesting(true);
    setDocTransTestResult(null);
    try {
      const result = await testSystemOptionsDocTrans(opts.doc_trans);
      setDocTransTestResult(result);
    } catch {
      setDocTransTestResult({ ok: false, message: t("system_options.messages.test_request_failed") });
    } finally {
      setDocTransTesting(false);
    }
  };

  const runDocTransInstall = async () => {
    setDocTransInstalling(true);
    setDocTransTestResult(null);
    try {
      const result = await installSystemOptionsDocTrans();
      if (result.doc_trans) {
        applyInstalledDocTrans(result.doc_trans);
      }
      setDocTransTestResult({
        ok: result.ok,
        message: result.message,
        engines: result.engines,
      });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.warning(result.message);
      }
    } catch {
      message.error(t("system_options.messages.doc_trans_engine_detect_failed"));
    } finally {
      setDocTransInstalling(false);
    }
  };

  const runLibreOfficeInstall = async () => {
    setDocTransInstallingLO(true);
    setDocTransTestResult(null);
    try {
      const result = await installLibreOfficeDocTrans();
      if (result.doc_trans) {
        applyInstalledDocTrans(result.doc_trans);
      }
      setDocTransTestResult({
        ok: result.ok,
        message: result.message,
        engines: result.engines,
      });
      if (result.ok) {
        message.success(result.message);
      } else {
        message.error(result.message);
      }
    } catch {
      message.error(t("system_options.messages.libreoffice_install_failed"));
    } finally {
      setDocTransInstallingLO(false);
    }
  };

  const moveEngine = (idx: number, dir: -1 | 1) => {
    setOpts((p) => {
      const order = [...p.doc_trans.engine_order];
      const j = idx + dir;
      if (j < 0 || j >= order.length) return p;
      [order[idx], order[j]] = [order[j], order[idx]];
      return { ...p, doc_trans: { ...p.doc_trans, engine_order: order } };
    });
  };

  const engineLabel = useCallback(
    (k: string) => {
      switch (k) {
        case "office":
          return t("system_options.doc_trans.engine_office");
        case "wps":
          return t("system_options.doc_trans.engine_wps");
        case "libreoffice":
          return t("system_options.doc_trans.engine_libreoffice");
        default:
          return k;
      }
    },
    [t],
  );

  const tabGeneral = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.general.language_section")}
      </Typography.Title>
      <SettingRow
        title={t("system_options.general.preferred_display_language")}
        description={
          <>
            {t("system_options.general.preferred_display_language_desc_prefix")}{" "}
            <Typography.Link href="https://translate.emby.media/" target="_blank" rel="noreferrer">
              {t("system_options.general.preferred_display_language_desc_link")}
            </Typography.Link>
          </>
        }
      >
        <Select
          style={{ minWidth: 200 }}
          options={DISPLAY_LANGUAGE_OPTIONS}
          value={adminLanguage}
          loading={languageSaving}
          disabled={languageSaving}
          placeholder={t("system_options.general.placeholder")}
          onChange={(v) => void handleAdminLanguageChange(v)}
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.general.startup_section")}
      </Typography.Title>
      <SettingRow
        title={t("system_options.general.start_on_boot")}
        description={t("system_options.general.start_on_boot_desc")}
      >
        <Switch
          checked={opts.general.start_on_boot}
          onChange={(v) => setOpts((p) => ({ ...p, general: { ...p.general, start_on_boot: v } }))}
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.general.open_browser_first_start")}
        description={t("system_options.general.open_browser_first_start_desc")}
      >
        <Switch
          checked={opts.general.open_browser_on_first_start}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              general: { ...p.general, open_browser_on_first_start: v },
            }))
          }
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.general.maintenance_section")}
      </Typography.Title>
      <SettingRow
        title={t("system_options.general.maintenance_mode")}
        description={t("system_options.general.maintenance_mode_desc")}
      >
        <Switch
          checked={opts.general.maintenance_mode}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              general: { ...p.general, maintenance_mode: v },
            }))
          }
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.general.advanced_section")}
      </Typography.Title>
      <SettingRow
        title={t("system_options.general.cache_path")}
        description={t("system_options.general.cache_path_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.general.cache_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              general: { ...p.general, cache_path: e.target.value },
            }))
          }
          placeholder={t("system_options.general.cache_path_placeholder")}
          suffix={
            <SearchOutlined
              style={{ color: "rgba(255,255,255,0.45)" }}
              onClick={() => message.info(t("system_options.messages.cache_path_manual_hint"))}
            />
          }
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.general.auto_update_section")}
      </Typography.Title>
      <SettingRow title={t("system_options.general.auto_update_enabled")}>
        <Switch
          checked={opts.general.auto_update_enabled}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              general: { ...p.general, auto_update_enabled: v },
            }))
          }
        />
      </SettingRow>
    </Space>
  );

  const tabPlayback = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t("system_options.playback.device_scope_note")}
      </Typography.Paragraph>
      <Typography.Title level={5} style={{ margin: 0 }}>
        {t("system_options.playback.video_section")}
      </Typography.Title>
      <SettingRow
        title={t("system_options.playback.home_stream_quality")}
        description={t("system_options.playback.home_stream_quality_desc")}
      >
        <Select
          showSearch
          optionFilterProp="label"
          style={{ minWidth: 260 }}
          listHeight={360}
          popupMatchSelectWidth={false}
          options={homeStreamQualityOptions}
          value={opts.playback.home_stream_quality}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              playback: { ...p.playback, home_stream_quality: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.playback.screen_orientation")}
        description={t("system_options.playback.screen_orientation_desc")}
      >
        <Select
          style={{ minWidth: 220 }}
          value={opts.playback.screen_orientation}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              playback: { ...p.playback, screen_orientation: v },
            }))
          }
          options={screenOrientationOptions}
        />
      </SettingRow>
    </Space>
  );

  const tabTranscoder = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <SettingRow
        title={t("system_options.transcoder.hardware_acceleration")}
        description={t("system_options.transcoder.hardware_acceleration_desc")}
      >
        <Select
          style={{ minWidth: 320 }}
          options={hardwareAccelerationOptions}
          value={opts.transcoder.hardware_acceleration}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: {
                ...p.transcoder,
                hardware_acceleration: v,
                enable_hardware_encoding: v === "none" ? false : true,
              },
            }))
          }
        />
      </SettingRow>
      {opts.transcoder.hardware_acceleration !== "none" ? (
        <SettingRow
          title={t("system_options.transcoder.enable_hardware_encoding")}
          description={t("system_options.transcoder.enable_hardware_encoding_desc")}
        >
          <Switch
            checked={opts.transcoder.enable_hardware_encoding}
            onChange={(v) =>
              setOpts((p) => ({
                ...p,
                transcoder: { ...p.transcoder, enable_hardware_encoding: v },
              }))
            }
          />
        </SettingRow>
      ) : null}
      <SettingRow
        title={t("system_options.transcoder.quality")}
        description={t("system_options.transcoder.quality_desc")}
      >
        <Select
          style={{ minWidth: 180 }}
          options={transcoderQualityOptions}
          value={opts.transcoder.quality}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, quality: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.temp_dir")}
        description={t("system_options.transcoder.temp_dir_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.transcoder.temp_dir}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, temp_dir: e.target.value },
            }))
          }
          placeholder={t("system_options.common.placeholder_server_default")}
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.download_temp_dir")}
        description={t("system_options.transcoder.download_temp_dir_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.transcoder.download_temp_dir}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, download_temp_dir: e.target.value },
            }))
          }
          placeholder={t("system_options.common.placeholder_server_default")}
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.throttle_buffer")}
        description={t("system_options.transcoder.throttle_buffer_desc")}
      >
        <InputNumber
          min={1}
          max={600}
          style={{ width: 120 }}
          value={opts.transcoder.throttle_buffer_seconds}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: {
                ...p.transcoder,
                throttle_buffer_seconds: typeof v === "number" ? v : p.transcoder.throttle_buffer_seconds,
              },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.bg_x264_preset")}
        description={t("system_options.transcoder.bg_x264_preset_desc")}
      >
        <Select
          style={{ minWidth: 260 }}
          options={x264PresetOptions}
          value={opts.transcoder.background_x264_preset}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, background_x264_preset: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.disable_video_transcode")}
        description={t("system_options.transcoder.disable_video_transcode_desc")}
      >
        <Switch
          checked={opts.transcoder.disable_video_stream_transcoding}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, disable_video_stream_transcoding: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.max_cpu_concurrent")}
        description={t("system_options.transcoder.max_cpu_concurrent_desc")}
      >
        <Select
          style={{ minWidth: 200 }}
          options={cpuConcurrentOptions}
          value={opts.transcoder.max_cpu_concurrent}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, max_cpu_concurrent: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.transcoder.max_bg_concurrent")}
        description={t("system_options.transcoder.max_bg_concurrent_desc")}
      >
        <Select
          style={{ minWidth: 120 }}
          options={BG_CONCURRENT_OPTIONS}
          value={opts.transcoder.max_background_concurrent}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, max_background_concurrent: v },
            }))
          }
        />
      </SettingRow>
    </Space>
  );

  const tabASR = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t("system_options.asr.intro")}
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={asrInstalling} onClick={() => void runAsrInstall()}>
            {t("system_options.actions.one_click_install")}
          </Button>
          <Button icon={<ApiOutlined />} loading={asrTesting} onClick={() => void runAsrTest()}>
            {t("system_options.actions.connection_test")}
          </Button>
        </Space>
      </Flex>
      {asrTestResult ? (
        <Typography.Text type={asrTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {asrTestResult.message}
        </Typography.Text>
      ) : null}
      <SettingRow
        title={t("system_options.asr.auto_on_scan")}
        description={t("system_options.asr.auto_on_scan_desc")}
      >
        <Switch
          checked={opts.recognition.asr.auto_on_scan}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, asr: { ...p.recognition.asr, auto_on_scan: v } },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.asr.ai_proofread")}
        description={t("system_options.asr.ai_proofread_desc")}
      >
        <Switch
          checked={opts.recognition.ai_proofread}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ai_proofread: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.asr.provider")} description={t("system_options.asr.provider_desc")}>
        <Select
          style={{ minWidth: 220 }}
          options={asrProviderOptions}
          value={opts.recognition.asr.provider}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, asr: { ...p.recognition.asr, provider: v } },
            }))
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.asr.whisper_path")} description={t("system_options.asr.whisper_path_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.asr.whisper_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, asr: { ...p.recognition.asr, whisper_path: e.target.value } },
            }))
          }
          placeholder="whisper"
        />
      </SettingRow>
      <SettingRow title={t("system_options.asr.extra_args")} description={t("system_options.asr.extra_args_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={(opts.recognition.asr.extra_args ?? []).join(" ")}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: {
                ...p.recognition,
                asr: {
                  ...p.recognition.asr,
                  extra_args: e.target.value.trim() ? e.target.value.trim().split(/\s+/) : [],
                },
              },
            }))
          }
          placeholder="--model small --language zh"
        />
      </SettingRow>
      <SettingRow title={t("system_options.asr.shell_cmd")} description={t("system_options.asr.shell_cmd_desc")} controlLayout="full">
        <Input.TextArea
          rows={4}
          value={opts.recognition.asr.shell}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, asr: { ...p.recognition.asr, shell: e.target.value } },
            }))
          }
          placeholder={'cd /d "{output_dir}" && python tools/asr/asr_to_vtt.py --engine whisper --input "{input}" --output-vtt "{output_vtt}"'}
        />
      </SettingRow>
    </Space>
  );

  const tabOCR = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t("system_options.ocr.intro")}
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={ocrInstalling} onClick={() => void runOcrInstall()}>
            {t("system_options.actions.one_click_install")}
          </Button>
          <Button icon={<ApiOutlined />} loading={ocrTesting} onClick={() => void runOcrTest()}>
            {t("system_options.actions.connection_test")}
          </Button>
        </Space>
      </Flex>
      {ocrTestResult ? (
        <Typography.Text type={ocrTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {ocrTestResult.message}
        </Typography.Text>
      ) : null}
      <SettingRow title={t("system_options.ocr.enable")} description={t("system_options.ocr.enable_desc")}>
        <Switch
          checked={opts.recognition.ocr.enabled}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, enabled: v } },
            }))
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.tesseract_path")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.tesseract_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, tesseract_path: e.target.value } },
            }))
          }
          placeholder="tesseract"
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.tessdata_dir")} description={t("system_options.ocr.tessdata_dir_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.tessdata_prefix}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, tessdata_prefix: e.target.value } },
            }))
          }
          placeholder={t("system_options.common.placeholder_system_default")}
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.languages")} description={t("system_options.ocr.languages_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.languages}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, languages: e.target.value } },
            }))
          }
          placeholder="chi_sim+eng"
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.python_path")} description={t("system_options.ocr.python_path_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.python_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, python_path: e.target.value } },
            }))
          }
          placeholder="python"
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.script_path")} description={t("system_options.ocr.script_path_desc")} controlLayout="full">
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.script_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, script_path: e.target.value } },
            }))
          }
          placeholder="tools/subtitle_ocr/bitmap_subtitle_ocr.py"
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.pgsrip_path")} description={t("system_options.ocr.pgsrip_path_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.pgsrip_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, pgsrip_path: e.target.value } },
            }))
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.mkvextract_path")} description={t("system_options.ocr.mkvextract_path_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.mkvextract_path}
          onChange={(e) =>
            setOpts((p) =>
              ({
                ...p,
                recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, mkvextract_path: e.target.value } },
              })
            )
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.ocr.mkvmerge_path")} description={t("system_options.ocr.mkvmerge_path_desc")}>
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.mkvmerge_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, mkvmerge_path: e.target.value } },
            }))
          }
        />
      </SettingRow>
    </Space>
  );

  const tabPhotoClassify = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t("system_options.photo_classify.intro")}
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={classifyInstalling} onClick={() => void runClassifyInstall()}>
            {t("system_options.actions.one_click_install")}
          </Button>
          <Button icon={<ApiOutlined />} loading={classifyTesting} onClick={() => void runClassifyTest()}>
            {t("system_options.actions.connection_test")}
          </Button>
        </Space>
      </Flex>
      {classifyTestResult ? (
        <Typography.Text type={classifyTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {classifyTestResult.message}
        </Typography.Text>
      ) : null}

      <SettingRow
        title={t("system_options.photo_classify.auto_on_scan")}
        description={t("system_options.photo_classify.auto_on_scan_desc")}
      >
        <Switch
          checked={opts.photo_classify.auto_on_scan}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, auto_on_scan: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_classify.engine")}
        description={t("system_options.photo_classify.engine_desc")}
      >
        <Select
          style={{ minWidth: 320 }}
          options={photoClassifyEngineOptions}
          value={opts.photo_classify.engine}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, engine: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_classify.python_path")}
        description={t("system_options.photo_classify.python_path_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_classify.python_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, python_path: e.target.value },
            }))
          }
          placeholder="tools/recognition/.venv/Scripts/python.exe"
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_classify.script_path")}
        description={t("system_options.photo_classify.script_path_desc")}
        controlLayout="full"
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_classify.script_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, script_path: e.target.value },
            }))
          }
          placeholder="tools/photo_classify/classify.py"
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_classify.model_path")}
        description={t("system_options.photo_classify.model_path_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_classify.model_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, model_path: e.target.value },
            }))
          }
          placeholder="tools/photo_classify/models/mobilenetv2-7.onnx"
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_classify.labels_path")}
        description={t("system_options.photo_classify.labels_path_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_classify.labels_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, labels_path: e.target.value },
            }))
          }
          placeholder="tools/photo_classify/imagenet_labels.txt"
        />
      </SettingRow>
    </Space>
  );

  const tabPhotoFace = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t("system_options.photo_face.intro")}
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={faceInstalling} onClick={() => void runFaceInstall()}>
            {t("system_options.actions.one_click_install")}
          </Button>
          <Button icon={<ApiOutlined />} loading={faceTesting} onClick={() => void runFaceTest()}>
            {t("system_options.actions.connection_test")}
          </Button>
        </Space>
      </Flex>
      {faceTestResult ? (
        <Typography.Text type={faceTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {faceTestResult.message}
        </Typography.Text>
      ) : null}

      <SettingRow
        title={t("system_options.photo_face.auto_on_scan")}
        description={t("system_options.photo_face.auto_on_scan_desc")}
      >
        <Switch
          checked={opts.photo_face.auto_on_scan}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              photo_face: { ...p.photo_face, auto_on_scan: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_face.similarity_threshold")}
        description={t("system_options.photo_face.similarity_threshold_desc")}
      >
        <InputNumber
          min={0.3}
          max={0.6}
          step={0.01}
          value={opts.photo_face.similarity_threshold}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              photo_face: { ...p.photo_face, similarity_threshold: typeof v === "number" ? v : 0.45 },
            }))
          }
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_face.python_path")}
        description={t("system_options.photo_face.python_path_desc")}
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_face.python_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_face: { ...p.photo_face, python_path: e.target.value },
            }))
          }
          placeholder="tools/recognition/.venv/Scripts/python.exe"
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.photo_face.script_path")}
        description={t("system_options.photo_face.script_path_desc")}
        controlLayout="full"
      >
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.photo_face.script_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              photo_face: { ...p.photo_face, script_path: e.target.value },
            }))
          }
          placeholder="tools/photo_face/detect.py"
        />
      </SettingRow>
    </Space>
  );

  const tabDocTrans = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary">{t("system_options.doc_trans.intro")}</Typography.Paragraph>
      <Flex gap="small" wrap="wrap">
        <Button icon={<SearchOutlined />} loading={docTransTesting} onClick={() => void runDocTransTest()}>
          {t("system_options.actions.detect_engines")}
        </Button>
        <Button icon={<ApiOutlined />} loading={docTransInstalling} onClick={() => void runDocTransInstall()}>
          {t("system_options.actions.auto_detect_write_config")}
        </Button>
        <Button icon={<CloudDownloadOutlined />} loading={docTransInstallingLO} onClick={() => void runLibreOfficeInstall()}>
          {t("system_options.actions.install_libreoffice")}
        </Button>
      </Flex>
      {docTransTestResult && (
        <>
          <Typography.Paragraph type={docTransTestResult.ok ? "success" : "warning"}>
            {docTransTestResult.message}
            {docTransTestResult.active_engine
              ? t("system_options.messages.doc_trans_active_engine", {
                  engine: engineLabel(docTransTestResult.active_engine),
                })
              : ""}
          </Typography.Paragraph>
          {docTransTestResult.engines && docTransTestResult.engines.length > 0 && (
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {docTransTestResult.engines.map((e) => (
                <Flex key={e.kind} justify="space-between" style={{ padding: "6px 10px", background: "rgba(255,255,255,0.04)", borderRadius: 6 }}>
                  <span>{e.label}</span>
                  <span style={{ color: e.available ? "#52c41a" : "rgba(255,255,255,0.45)" }}>
                    {e.available ? t("system_options.common.available") : e.message || t("system_options.common.unavailable")}
                  </span>
                </Flex>
              ))}
            </div>
          )}
        </>
      )}
      <SettingRow
        title={t("system_options.doc_trans.engine_priority")}
        description={t("system_options.doc_trans.engine_priority_desc")}
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          {opts.doc_trans.engine_order.map((k, i) => (
            <Flex key={k} gap={8} align="center">
              <span style={{ width: 24, opacity: 0.5 }}>{i + 1}.</span>
              <span style={{ flex: 1 }}>{engineLabel(k)}</span>
              <Button size="small" disabled={i === 0} onClick={() => moveEngine(i, -1)}>↑</Button>
              <Button size="small" disabled={i === opts.doc_trans.engine_order.length - 1} onClick={() => moveEngine(i, 1)}>↓</Button>
            </Flex>
          ))}
        </Space>
      </SettingRow>
      <SettingRow title={t("system_options.doc_trans.enable")} description={t("system_options.doc_trans.enable_desc")}>
        <Switch
          checked={opts.doc_trans.enabled}
          onChange={(v) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, enabled: v } }))}
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.doc_trans.libreoffice_path")}
        description={t("system_options.doc_trans.libreoffice_path_desc")}
      >
        <Input
          value={opts.doc_trans.libreoffice_path}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              doc_trans: { ...p.doc_trans, libreoffice_path: e.target.value, soffice_path: e.target.value },
            }))
          }
          placeholder="tools/doctran/LibreOffice/program/soffice.exe"
        />
      </SettingRow>
      <SettingRow
        title={t("system_options.doc_trans.office_path")}
        description={t("system_options.doc_trans.office_path_desc")}
      >
        <Input
          value={opts.doc_trans.office_path}
          onChange={(e) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, office_path: e.target.value } }))}
          placeholder=""
        />
      </SettingRow>
      <SettingRow title={t("system_options.doc_trans.wps_path")} description={t("system_options.doc_trans.wps_path_desc")}>
        <Input
          value={opts.doc_trans.wps_path}
          onChange={(e) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, wps_path: e.target.value } }))}
          placeholder=""
        />
      </SettingRow>
      <SettingRow title={t("system_options.doc_trans.cache_dir")} description={t("system_options.doc_trans.cache_dir_desc")}>
        <Input
          value={opts.doc_trans.cache_dir}
          onChange={(e) =>
            setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, cache_dir: e.target.value } }))
          }
          placeholder=""
        />
      </SettingRow>
      <SettingRow title={t("system_options.doc_trans.cache_ttl")} description={t("system_options.doc_trans.cache_ttl_desc")}>
        <InputNumber
          min={1}
          max={365}
          value={opts.doc_trans.cache_ttl_days}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              doc_trans: { ...p.doc_trans, cache_ttl_days: typeof v === "number" ? v : 30 },
            }))
          }
        />
      </SettingRow>
      <SettingRow title={t("system_options.doc_trans.timeout")} description={t("system_options.doc_trans.timeout_desc")}>
        <InputNumber
          min={30}
          max={600}
          value={opts.doc_trans.timeout_seconds}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              doc_trans: { ...p.doc_trans, timeout_seconds: typeof v === "number" ? v : 180 },
            }))
          }
        />
      </SettingRow>
    </Space>
  );

  return (
    <Card loading={loading}>
      <Flex justify="flex-end" style={{ marginBottom: 16 }}>
        <Space>
          <Button onClick={reset} disabled={!dirty || saving}>
            {t("system_options.actions.reset")}
          </Button>
          <Button type="primary" onClick={() => void save()} disabled={!dirty} loading={saving}>
            {t("system_options.actions.save_changes")}
          </Button>
        </Space>
      </Flex>
      <Tabs
        defaultActiveKey="general"
        items={[
          { key: "general", label: t("system_options.tab.general"), children: tabGeneral },
          { key: "playback", label: t("system_options.tab.playback"), children: tabPlayback },
          { key: "transcoder", label: t("system_options.tab.transcoder"), children: tabTranscoder },
          { key: "asr", label: t("system_options.tab.asr"), children: tabASR },
          { key: "ocr", label: t("system_options.tab.ocr"), children: tabOCR },
          { key: "photo-classify", label: t("system_options.tab.photo_classify"), children: tabPhotoClassify },
          { key: "photo-face", label: t("system_options.tab.photo_face"), children: tabPhotoFace },
          { key: "doc-trans", label: t("system_options.tab.doc_trans"), children: tabDocTrans },
        ]}
      />
    </Card>
  );
}
