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
  type SystemOptions,
  type RecognitionTestResult,
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

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const merged = mergeSystemOptions(await fetchSystemOptions());
      setOpts(merged);
      setBaseline(merged);
      setAsrTestResult(null);
      setOcrTestResult(null);
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
        ]}
      />
    </Card>
  );
}
