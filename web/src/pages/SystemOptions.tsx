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
  type SystemOptions,
  type RecognitionTestResult,
  type DocTransTestResult,
} from "../api/client";

function defaultSystemOptions(): SystemOptions {
  return {
    general: {
      display_language: "zh-Hans",
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
      disable_video_stream_transcoding: false,
      max_cpu_concurrent: "unlimited",
      max_background_concurrent: "1",
    },
    recognition: {
      asr: {
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
        script_path: "",
        pgsrip_path: "",
        mkvextract_path: "",
        mkvmerge_path: "",
      },
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
    },
    photo_classify: { ...base.photo_classify, ...(data.photo_classify ?? {}) },
    photo_face: { ...base.photo_face, ...(data.photo_face ?? {}) },
    doc_trans: { ...base.doc_trans, ...(data.doc_trans ?? {}) },
  };
}

function buildHomeStreamQualityOptions(): { value: string; label: string }[] {
  const out: { value: string; label: string }[] = [{ value: "auto", label: "自动" }];
  for (const m of [200, 160, 140, 120, 100, 80, 60, 40]) {
    out.push({ value: `4k-${m}mbps`, label: `4K · ${m} Mbps` });
  }
  for (const m of [60, 50, 40, 30, 25, 20, 15, 12, 10, 8, 6, 5]) {
    out.push({ value: `1080p-${m}mbps`, label: `1080p · ${m} Mbps` });
  }
  for (const m of [8, 6, 4, 3, 2]) {
    out.push({ value: `720p-${m}mbps`, label: `720p · ${m} Mbps` });
  }
  for (const m of [4, 3, 2]) {
    out.push({ value: `480p-${m}mbps`, label: `480p · ${m} Mbps` });
  }
  out.push({ value: "480p-1_5mbps", label: "480p · 1.5 Mbps" });
  return out;
}

const HOME_STREAM_QUALITY_OPTIONS = buildHomeStreamQualityOptions();

const DISPLAY_LANGUAGE_OPTIONS = [
  { value: "zh-Hans", label: "简体中文" },
  { value: "zh-Hant", label: "繁體中文" },
  { value: "en", label: "English" },
  { value: "ja", label: "日本語" },
  { value: "ko", label: "한국어" },
];

const TRANSCODER_QUALITY_OPTIONS = [
  { value: "auto", label: "自动" },
  { value: "max", label: "最高" },
  { value: "high", label: "高" },
  { value: "medium", label: "中" },
  { value: "low", label: "低" },
];

const X264_PRESET_OPTIONS = [
  { value: "ultrafast", label: "极快 (ultrafast)" },
  { value: "superfast", label: "超快 (superfast)" },
  { value: "veryfast", label: "非常快 (veryfast)" },
  { value: "faster", label: "更快 (faster)" },
  { value: "fast", label: "快 (fast)" },
  { value: "medium", label: "中等 (medium)" },
  { value: "slow", label: "慢 (slow)" },
  { value: "slower", label: "更慢 (slower)" },
  { value: "veryslow", label: "非常慢 (veryslow)" },
];

const CPU_CONCURRENT_OPTIONS = [
  { value: "unlimited", label: "无限制" },
  ...Array.from({ length: 16 }, (_, i) => ({
    value: String(i + 1),
    label: String(i + 1),
  })),
];

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

const ASR_PROVIDER_OPTIONS = [
  { value: "none", label: "关闭" },
  { value: "whisper_cli", label: "Whisper CLI" },
  { value: "shell", label: "Shell 脚本" },
];

const PHOTO_CLASSIFY_ENGINE_OPTIONS = [
  { value: "auto", label: "自动（有 ONNX 模型时用 ONNX，否则启发式）" },
  { value: "heuristic", label: "启发式（Go + 颜色/构图，无需 Python）" },
  { value: "onnx", label: "ONNX（MobileNet + Python）" },
];

export default function SystemOptionsPage() {
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
      const merged = mergeSystemOptions(await fetchSystemOptions());
      setOpts(merged);
      setBaseline(merged);
      setAsrTestResult(null);
      setOcrTestResult(null);
      setClassifyTestResult(null);
    } catch {
      message.error("加载系统选项失败");
    } finally {
      setLoading(false);
    }
  }, []);

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
      message.success("已保存");
    } catch {
      message.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    setOpts(baseline);
    setAsrTestResult(null);
    setOcrTestResult(null);
    setClassifyTestResult(null);
    message.info("已恢复为上次加载的值");
  };

  const runAsrTest = async () => {
    setAsrTesting(true);
    setAsrTestResult(null);
    try {
      const result = await testSystemOptionsASR(opts.recognition.asr);
      setAsrTestResult(result);
    } catch {
      setAsrTestResult({ ok: false, message: "测试请求失败" });
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
      setOcrTestResult({ ok: false, message: "测试请求失败" });
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
      message.error("ASR 安装请求失败（可能超时，请查看服务器日志）");
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
      message.error("OCR 安装请求失败（可能超时，请查看服务器日志）");
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
      setClassifyTestResult({ ok: false, message: "测试请求失败" });
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
      message.error("智能分类安装请求失败（可能超时，请查看服务器日志）");
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
      setFaceTestResult({ ok: false, message: "测试请求失败" });
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
      message.error("人脸检测安装请求失败（可能超时，请查看服务器日志）");
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
      setDocTransTestResult({ ok: false, message: "测试请求失败" });
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
      message.error("引擎检测失败");
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
      message.error("LibreOffice 安装失败");
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

  const engineLabel = (k: string) => {
    switch (k) {
      case "office": return "Microsoft Office";
      case "wps": return "WPS Office";
      case "libreoffice": return "LibreOffice";
      default: return k;
    }
  };

  const tabGeneral = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Title level={5} style={{ margin: 0 }}>
        语言
      </Typography.Title>
      <SettingRow
        title="首选显示语言"
        description={
          <>
            界面与文案的完善是一项持续进行的工作。{" "}
            <Typography.Link href="https://translate.emby.media/" target="_blank" rel="noreferrer">
              了解如何参与翻译贡献。
            </Typography.Link>
          </>
        }
      >
        <Select
          style={{ minWidth: 200 }}
          options={DISPLAY_LANGUAGE_OPTIONS}
          value={opts.general.display_language}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              general: { ...p.general, display_language: v },
            }))
          }
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        启动选项
      </Typography.Title>
      <SettingRow
        title="开机自启"
        description="这将在 Windows 启动时启动托盘图标（若使用桌面安装版）。若已配置为 Windows 服务，请关闭此项并改为由服务开机自启。"
      >
        <Switch
          checked={opts.general.start_on_boot}
          onChange={(v) => setOpts((p) => ({ ...p, general: { ...p.general, start_on_boot: v } }))}
        />
      </SettingRow>
      <SettingRow
        title="服务器首次启动时打开浏览器访问 Web 应用"
        description="仅在首次启动时打开默认浏览器进入 Web 界面；进程重启后不会再次自动打开。"
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
        维护模式
      </Typography.Title>
      <SettingRow title="将服务器设置为维护模式" description="用户将只会看到维护模式消息。">
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
        高级
      </Typography.Title>
      <SettingRow
        title="缓存路径"
        description="为服务器缓存文件（例如图像）指定自定义位置。留空可使用服务器默认值。"
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
          placeholder="默认"
          suffix={
            <SearchOutlined
              style={{ color: "rgba(255,255,255,0.45)" }}
              onClick={() => message.info("请在服务器可访问的路径下手动填写目录")}
            />
          }
        />
      </SettingRow>

      <Divider style={{ margin: "8px 0" }} />

      <Typography.Title level={5} style={{ margin: 0 }}>
        自动更新
      </Typography.Title>
      <SettingRow title="启用服务器自动更新">
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
        这些设置会影响在此设备上播放的所有用户。
      </Typography.Paragraph>
      <Typography.Title level={5} style={{ margin: 0 }}>
        视频
      </Typography.Title>
      <SettingRow title="家庭流传输质量" description="选择远程或本地播放时的默认最大码率上限（与服务器转码能力相关）。">
        <Select
          showSearch
          optionFilterProp="label"
          style={{ minWidth: 260 }}
          listHeight={360}
          popupMatchSelectWidth={false}
          options={HOME_STREAM_QUALITY_OPTIONS}
          value={opts.playback.home_stream_quality}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              playback: { ...p.playback, home_stream_quality: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow title="屏幕方向" description="客户端全屏播放时的显示方向策略。">
        <Select
          style={{ minWidth: 220 }}
          value={opts.playback.screen_orientation}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              playback: { ...p.playback, screen_orientation: v },
            }))
          }
          options={[
            { value: "auto", label: "自动" },
            { value: "lock_landscape", label: "锁定横屏" },
            { value: "device", label: "使用设备设置" },
          ]}
        />
      </SettingRow>
    </Space>
  );

  const tabTranscoder = (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <SettingRow title="转码器质量" description="转码器使用的质量配置文件。">
        <Select
          style={{ minWidth: 180 }}
          options={TRANSCODER_QUALITY_OPTIONS}
          value={opts.transcoder.quality}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, quality: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow title="转码器临时目录" description="为临时文件转码时使用的目录。">
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.transcoder.temp_dir}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, temp_dir: e.target.value },
            }))
          }
          placeholder="留空使用服务器默认"
        />
      </SettingRow>
      <SettingRow title="下载临时目录" description="在客户端下载之前存储转码下载时使用的目录。">
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.transcoder.download_temp_dir}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              transcoder: { ...p.transcoder, download_temp_dir: e.target.value },
            }))
          }
          placeholder="留空使用服务器默认"
        />
      </SettingRow>
      <SettingRow title="转码器默认的节流缓冲器" description="在对转码器进行节流前缓冲所用的秒数。">
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
        title="后台转码 x264 预设"
        description="x264 预设值用于后台转码（同步和媒体优化器）。值越小，视频质量越好、文件越小，但处理时间更长。"
      >
        <Select
          style={{ minWidth: 260 }}
          options={X264_PRESET_OPTIONS}
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
        title="禁用视频流转码"
        description="在转码操作中禁用视频流的转码。设置后，转码器仍然可以转码音频或重新封装视频。"
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
      <SettingRow title="最大 CPU 并发转码数" description="限制服务器使用 CPU 进行视频转码的并发数量。">
        <Select
          style={{ minWidth: 200 }}
          options={CPU_CONCURRENT_OPTIONS}
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
        title="最大后台视频并发转码数"
        description="限制服务器可用于优化器和下载的视频转码的并发数量。"
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
        语音识别（ASR）配置保存在 config.yml 的 subtitle.asr 段。若未安装依赖，可使用「一键安装」自动部署到
        tools/recognition/（Python 虚拟环境）。
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={asrInstalling} onClick={() => void runAsrInstall()}>
            一键安装
          </Button>
          <Button icon={<ApiOutlined />} loading={asrTesting} onClick={() => void runAsrTest()}>
            连接测试
          </Button>
        </Space>
      </Flex>
      {asrTestResult ? (
        <Typography.Text type={asrTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {asrTestResult.message}
        </Typography.Text>
      ) : null}
      <SettingRow
        title="Provider"
        description="无字幕时可选自动语音识别。Whisper CLI 直接调用 whisper 命令；Shell 使用自定义脚本（支持 {input}、{output_dir}、{output_vtt} 占位符）。"
      >
        <Select
          style={{ minWidth: 220 }}
          options={ASR_PROVIDER_OPTIONS}
          value={opts.recognition.asr.provider}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, asr: { ...p.recognition.asr, provider: v } },
            }))
          }
        />
      </SettingRow>
      <SettingRow title="Whisper 路径" description="provider 为 whisper_cli 时使用；留空默认为 whisper。">
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
      <SettingRow title="额外参数" description="Whisper CLI 附加参数，空格分隔（例如 --model small --language zh）。">
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
      <SettingRow title="Shell 命令" description="provider 为 shell 时使用；可引用 tools/asr/asr_to_vtt.py 等脚本。" controlLayout="full">
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
        字符识别（OCR）配置保存在 config.yml 的 subtitle.graphical_ocr 段。若未安装依赖，可使用「一键安装」自动部署 Python 包及
        tools/tesseract/（Windows）。
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={ocrInstalling} onClick={() => void runOcrInstall()}>
            一键安装
          </Button>
          <Button icon={<ApiOutlined />} loading={ocrTesting} onClick={() => void runOcrTest()}>
            连接测试
          </Button>
        </Space>
      </Flex>
      {ocrTestResult ? (
        <Typography.Text type={ocrTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {ocrTestResult.message}
        </Typography.Text>
      ) : null}
      <SettingRow title="启用 OCR" description="对 PGS、VobSub 等位图字幕进行 Tesseract OCR 提取。">
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
      <SettingRow title="Tesseract 路径">
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
      <SettingRow title="Tessdata 目录" description="TESSDATA_PREFIX，可选。">
        <Input
          style={{ width: 480, maxWidth: "100%" }}
          value={opts.recognition.ocr.tessdata_prefix}
          onChange={(e) =>
            setOpts((p) => ({
              ...p,
              recognition: { ...p.recognition, ocr: { ...p.recognition.ocr, tessdata_prefix: e.target.value } },
            }))
          }
          placeholder="留空使用系统默认"
        />
      </SettingRow>
      <SettingRow title="识别语言" description="Tesseract 语言包，多个用 + 连接。">
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
      <SettingRow title="Python 路径" description="运行 OCR 脚本的解释器；留空在 Windows 用 python，其他平台用 python3。">
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
      <SettingRow title="OCR 脚本路径" description="bitmap_subtitle_ocr.py 的绝对或相对路径。" controlLayout="full">
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
      <SettingRow title="pgsrip 路径" description="可选，PGS 字幕预处理工具。">
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
      <SettingRow title="mkvextract 路径" description="可选，Matroska 轨道提取。">
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
      <SettingRow title="mkvmerge 路径" description="可选，Matroska 工具。">
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
        图片库 AI 智能分类配置保存在 config.yml 的 photo_classify 段。启发式引擎由 Go 内置；ONNX 引擎需 Python 依赖与
        MobileNet 模型。可使用「一键安装」自动部署到 tools/photo_classify/（共用 tools/recognition/.venv）。
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={classifyInstalling} onClick={() => void runClassifyInstall()}>
            一键安装
          </Button>
          <Button icon={<ApiOutlined />} loading={classifyTesting} onClick={() => void runClassifyTest()}>
            连接测试
          </Button>
        </Space>
      </Flex>
      {classifyTestResult ? (
        <Typography.Text type={classifyTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {classifyTestResult.message}
        </Typography.Text>
      ) : null}

      <SettingRow
        title="扫描时自动分类"
        description="导入或扫描图片库时，为新图片自动加入 photo_classify_task 队列。"
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
        title="分类引擎"
        description="auto：检测到 ONNX 模型时使用深度学习增强；heuristic：仅使用内置启发式；onnx：强制使用 ONNX。"
      >
        <Select
          style={{ minWidth: 320 }}
          options={PHOTO_CLASSIFY_ENGINE_OPTIONS}
          value={opts.photo_classify.engine}
          onChange={(v) =>
            setOpts((p) => ({
              ...p,
              photo_classify: { ...p.photo_classify, engine: v },
            }))
          }
        />
      </SettingRow>
      <SettingRow title="Python 路径" description="运行 classify.py 的解释器；留空在 Windows 用 python，其他平台用 python3。">
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
      <SettingRow title="分类脚本路径" description="classify.py 的相对或绝对路径。" controlLayout="full">
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
      <SettingRow title="ONNX 模型路径" description="MobileNetV2 ONNX 模型；auto/onnx 引擎需要。">
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
      <SettingRow title="ImageNet 标签文件" description="每行一个类别名，与模型输出索引对应。">
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
        人脸检测与聚类配置保存在 config.yml 的 photo_face 段，使用 InsightFace 检测人脸并自动聚类为「人物」（共用
        tools/recognition/.venv）。
      </Typography.Paragraph>

      <Flex justify="flex-end" wrap="wrap" gap={8}>
        <Space wrap>
          <Button icon={<CloudDownloadOutlined />} loading={faceInstalling} onClick={() => void runFaceInstall()}>
            一键安装
          </Button>
          <Button icon={<ApiOutlined />} loading={faceTesting} onClick={() => void runFaceTest()}>
            连接测试
          </Button>
        </Space>
      </Flex>
      {faceTestResult ? (
        <Typography.Text type={faceTestResult.ok ? "success" : "danger"} style={{ fontSize: 13 }}>
          {faceTestResult.message}
        </Typography.Text>
      ) : null}

      <SettingRow
        title="扫描时自动人脸检测"
        description="导入或扫描图片库时，为新图片自动加入 photo_face_task 队列。"
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
        title="聚类相似度阈值"
        description="人脸特征余弦相似度达到该值时视为同一人，范围 0.3–0.6，默认 0.45。"
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
      <SettingRow title="Python 路径" description="运行 detect.py 的解释器；留空则使用智能分类中的 Python 路径。">
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
      <SettingRow title="人脸检测脚本路径" description="detect.py 的相对或绝对路径。" controlLayout="full">
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
      <Typography.Paragraph type="secondary">
        Office 文档在线预览按引擎优先级依次尝试：Microsoft Office、WPS、LibreOffice。
        LibreOffice 默认部署在 <Typography.Text code>tools/doctran</Typography.Text>。
      </Typography.Paragraph>
      <Flex gap="small" wrap="wrap">
        <Button icon={<SearchOutlined />} loading={docTransTesting} onClick={() => void runDocTransTest()}>
          检测引擎
        </Button>
        <Button icon={<ApiOutlined />} loading={docTransInstalling} onClick={() => void runDocTransInstall()}>
          自动检测并写入配置
        </Button>
        <Button icon={<CloudDownloadOutlined />} loading={docTransInstallingLO} onClick={() => void runLibreOfficeInstall()}>
          一键安装 LibreOffice
        </Button>
      </Flex>
      {docTransTestResult && (
        <>
          <Typography.Paragraph type={docTransTestResult.ok ? "success" : "warning"}>
            {docTransTestResult.message}
            {docTransTestResult.active_engine ? `（当前首选: ${engineLabel(docTransTestResult.active_engine)}）` : ""}
          </Typography.Paragraph>
          {docTransTestResult.engines && docTransTestResult.engines.length > 0 && (
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {docTransTestResult.engines.map((e) => (
                <Flex key={e.kind} justify="space-between" style={{ padding: "6px 10px", background: "rgba(255,255,255,0.04)", borderRadius: 6 }}>
                  <span>{e.label}</span>
                  <span style={{ color: e.available ? "#52c41a" : "rgba(255,255,255,0.45)" }}>
                    {e.available ? "可用" : e.message || "不可用"}
                  </span>
                </Flex>
              ))}
            </div>
          )}
        </>
      )}
      <SettingRow title="引擎优先级" description="从上到下依次尝试；仅使用已安装且可用的引擎。">
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
      <SettingRow title="启用文档转换" description="关闭后 Office 格式仅支持下载。">
        <Switch
          checked={opts.doc_trans.enabled}
          onChange={(v) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, enabled: v } }))}
        />
      </SettingRow>
      <SettingRow title="LibreOffice 路径" description="soffice 可执行文件。">
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
      <SettingRow title="Office 路径（可选）" description="留空则自动检测 WINWORD.EXE 所在目录。">
        <Input
          value={opts.doc_trans.office_path}
          onChange={(e) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, office_path: e.target.value } }))}
          placeholder=""
        />
      </SettingRow>
      <SettingRow title="WPS 路径（可选）" description="WPS office6 目录，留空则自动检测。">
        <Input
          value={opts.doc_trans.wps_path}
          onChange={(e) => setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, wps_path: e.target.value } }))}
          placeholder=""
        />
      </SettingRow>
      <SettingRow title="转换缓存目录" description="留空则使用 data/preview/documents/convert。">
        <Input
          value={opts.doc_trans.cache_dir}
          onChange={(e) =>
            setOpts((p) => ({ ...p, doc_trans: { ...p.doc_trans, cache_dir: e.target.value } }))
          }
          placeholder=""
        />
      </SettingRow>
      <SettingRow title="缓存有效期（天）" description="超过此时间的转换缓存将被忽略并重新转换。">
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
      <SettingRow title="转换超时（秒）" description="单次 LibreOffice 转换的最长等待时间。">
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
            重置
          </Button>
          <Button type="primary" onClick={() => void save()} disabled={!dirty} loading={saving}>
            保存更改
          </Button>
        </Space>
      </Flex>
      <Tabs
        defaultActiveKey="general"
        items={[
          { key: "general", label: "通用", children: tabGeneral },
          { key: "playback", label: "播放", children: tabPlayback },
          { key: "transcoder", label: "转码器", children: tabTranscoder },
          { key: "asr", label: "语音识别", children: tabASR },
          { key: "ocr", label: "字符识别", children: tabOCR },
          { key: "photo-classify", label: "智能分类", children: tabPhotoClassify },
          { key: "photo-face", label: "人脸聚类", children: tabPhotoFace },
          { key: "doc-trans", label: "文档转换", children: tabDocTrans },
        ]}
      />
    </Card>
  );
}
