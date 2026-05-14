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
import { SearchOutlined } from "@ant-design/icons";
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
  type SystemOptions,
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

export default function SystemOptionsPage() {
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [opts, setOpts] = useState<SystemOptions>(() => defaultSystemOptions());
  const [baseline, setBaseline] = useState<SystemOptions>(() => defaultSystemOptions());

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await fetchSystemOptions();
      setOpts(data);
      setBaseline(data);
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
      setOpts(saved);
      setBaseline(saved);
      message.success("已保存");
    } catch {
      message.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  const reset = () => {
    setOpts(baseline);
    message.info("已恢复为上次加载的值");
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
        ]}
      />
    </Card>
  );
}
