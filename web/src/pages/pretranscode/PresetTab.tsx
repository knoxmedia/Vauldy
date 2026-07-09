import { useEffect, useState } from "react";
import {
  Button,
  Card,
  Collapse,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Row,
  Col,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import {
  PlusOutlined,
  CopyOutlined,
  EditOutlined,
  DeleteOutlined,
  SettingOutlined,
} from "@ant-design/icons";
import {
  fetchPresets,
  createPreset,
  updatePreset,
  deletePreset,
  clonePreset,
  togglePreset,
  fetchAvailableEncoders,
  type Preset,
  type Rendition,
  type EncoderInfo,
} from "../../api/pretranscode";
import { useT } from "../../i18n";

const { Text } = Typography;

const FORMATS: Array<{ value: Preset["output_format"]; label: string }> = [
  { value: "hls", label: "HLS" },
  { value: "mp4", label: "MP4" },
  { value: "dash", label: "DASH" },
  { value: "flv", label: "FLV" },
];

const ENCRYPTIONS: Array<{ value: Preset["encryption_mode"]; label: string }> = [
  { value: "none", label: "none" },
  { value: "aes128", label: "AES-128" },
  { value: "powerdrm", label: "PowerDRM" },
  { value: "drm", label: "DRM (CENC)" },
];

const OUTPUT_DIR_MODES: Array<{ value: NonNullable<Preset["output_dir_mode"]>; labelKey: string }> = [
  { value: "source", labelKey: "pretranscode.preset.output_dir_mode_source" },
  { value: "custom", labelKey: "pretranscode.preset.output_dir_mode_custom" },
];

function encoderOptions(encoders: EncoderInfo[]): Array<{ value: string; label: string; options?: Array<{ value: string; label: string }> }> {
  if (!encoders.length) return [];
  const h264 = encoders.filter((e) => e.family === "h264");
  const h265 = encoders.filter((e) => e.family === "h265");
  return [
    {
      value: "h264",
      label: "H.264",
      options: h264.map((e) => ({ value: e.id, label: e.name + (e.type === "hardware" ? " ⚡" : "") })),
    },
    {
      value: "h265",
      label: "H.265",
      options: h265.map((e) => ({ value: e.id, label: e.name + (e.type === "hardware" ? " ⚡" : "") })),
    },
  ];
}

const AUDIO_CODECS = ["aac", "mp3", "copy"];
const X264_PRESETS = ["ultrafast", "superfast", "veryfast", "faster", "fast", "medium", "slow", "slower", "veryslow"];
const PROFILES = ["baseline", "main", "high"];

type ResolutionPreset = "original" | "custom" | "4k" | "1080p" | "720p" | "540p" | "480p" | "360p";
type BitrateMode = "average" | "crf";

type ResolutionOption = {
  value: ResolutionPreset;
  height: number;
  name: string;
  video_bitrate: string;
  audio_bitrate: string;
};

const RESOLUTION_OPTIONS: ResolutionOption[] = [
  { value: "original", height: 0, name: "original", video_bitrate: "5000k", audio_bitrate: "128k" },
  { value: "4k", height: 2160, name: "4K", video_bitrate: "12000k", audio_bitrate: "192k" },
  { value: "1080p", height: 1080, name: "1080p", video_bitrate: "5000k", audio_bitrate: "128k" },
  { value: "720p", height: 720, name: "720p", video_bitrate: "2800k", audio_bitrate: "128k" },
  { value: "540p", height: 540, name: "540p", video_bitrate: "2000k", audio_bitrate: "128k" },
  { value: "480p", height: 480, name: "480p", video_bitrate: "1400k", audio_bitrate: "96k" },
  { value: "360p", height: 360, name: "360p", video_bitrate: "800k", audio_bitrate: "96k" },
];

function guessResolutionPreset(r: Pick<Rendition, "height" | "width" | "name">): ResolutionPreset {
  if (r.height === 0 || r.name === "original" || r.name === "原始") return "original";
  const byHeight = RESOLUTION_OPTIONS.find((o) => o.value !== "original" && o.height === r.height);
  if (byHeight && (!r.width || r.name === byHeight.name)) return byHeight.value;
  return "custom";
}

type RenditionEntry = Rendition & {
  resolution_preset?: ResolutionPreset;
  bitrate_mode?: BitrateMode;
  video_crf?: number;
};

function formatVideoBitrate(bitrate: string): string {
  const trimmed = bitrate.trim();
  const m = trimmed.match(/^(\d+(?:\.\d+)?)\s*([kKmM])?b?(?:ps)?$/i);
  if (!m) return trimmed;
  let kbps = parseFloat(m[1]);
  const unit = (m[2] || "k").toLowerCase();
  if (unit === "m") kbps *= 1000;
  return `${Math.round(kbps)} Kbps`;
}

function renditionSizeLabel(t: (key: string) => string, r: RenditionEntry): string {
  const preset = r.resolution_preset ?? guessResolutionPreset(r);
  if (preset === "original" || r.height === 0) {
    return t("pretranscode.preset.rendition_original");
  }
  if (preset === "custom") {
    if (r.width && r.width > 0 && r.height) return `${r.width} x ${r.height}`;
    if (r.height) return String(r.height);
    return t("pretranscode.preset.rendition_custom");
  }
  return String(r.height);
}

function renditionQualityLabel(r: RenditionEntry, presetCrf = 0): string {
  const mode = r.bitrate_mode ?? ((r.video_crf ?? presetCrf) > 0 ? "crf" : "average");
  if (mode === "crf") {
    const crf = r.video_crf ?? presetCrf;
    return crf > 0 ? `${crf} CRF` : "CRF";
  }
  return formatVideoBitrate(r.video_bitrate);
}

function enrichRenditionEntry(r: Rendition, preset?: Partial<Preset>): RenditionEntry {
  const presetCrf = preset?.video_crf ?? 0;
  return {
    ...r,
    resolution_preset: guessResolutionPreset(r),
    bitrate_mode: presetCrf > 0 ? "crf" : "average",
    video_crf: presetCrf > 0 ? presetCrf : undefined,
  };
}

type RenditionFormValues = Rendition & {
  resolution_preset?: ResolutionPreset;
  bitrate_mode?: BitrateMode;
  video_max_keyframe?: number;
  video_fps?: number;
  encryption_mode?: Preset["encryption_mode"];
  video_crf?: number;
  video_maxrate?: string;
  video_bufsize?: string;
  video_preset?: string;
  video_profile?: string;
  video_pix_fmt?: string;
  audio_codec?: string;
  audio_channels?: number;
  audio_sample_rate?: number;
};

function buildDefaultRenditions(format: Preset["output_format"], preset?: Partial<Preset>): RenditionEntry[] {
  const videoCrf = preset?.video_crf ?? 23;
  const mk = (opt: ResolutionOption): RenditionEntry => ({
    name: opt.name,
    height: opt.height,
    video_bitrate: opt.video_bitrate,
    audio_bitrate: opt.audio_bitrate,
    resolution_preset: opt.value,
    bitrate_mode: videoCrf > 0 ? "crf" : "average",
    video_crf: videoCrf > 0 ? videoCrf : undefined,
  });
  if (format === "mp4" || format === "flv") {
    const opt720 = RESOLUTION_OPTIONS.find((o) => o.value === "720p")!;
    return [mk(opt720)];
  }
  // HLS / DASH
  const targets = ["480p", "720p", "1080p"] as const;
  return targets.map((v) => mk(RESOLUTION_OPTIONS.find((o) => o.value === v)!));
}

function defaultRenditionFormValues(preset?: Partial<Preset>): RenditionFormValues {
  const videoCrf = preset?.video_crf ?? 23;
  return {
    resolution_preset: "720p",
    bitrate_mode: videoCrf > 0 ? "crf" : "average",
    height: 720,
    name: "720p",
    video_bitrate: "2800k",
    audio_bitrate: "128k",
    video_crf: videoCrf,
    video_max_keyframe: preset?.video_gop ? preset.video_gop * 25 : 250,
    video_fps: 25,
    encryption_mode: preset?.encryption_mode ?? "none",
    video_maxrate: preset?.video_maxrate,
    video_bufsize: preset?.video_bufsize,
    video_preset: preset?.video_preset ?? "veryfast",
    video_profile: preset?.video_profile,
    video_pix_fmt: preset?.video_pix_fmt,
    audio_codec: preset?.audio_codec ?? "aac",
    audio_channels: preset?.audio_channels ?? 2,
    audio_sample_rate: preset?.audio_sample_rate ?? 48000,
  };
}

function splitRenditionValues(values: RenditionFormValues): {
  rendition: RenditionEntry;
  presetEncoding: Partial<Preset>;
} {
  const {
    resolution_preset,
    bitrate_mode,
    name,
    height,
    width,
    video_bitrate,
    audio_bitrate,
    video_crf,
    video_max_keyframe,
    video_fps,
    encryption_mode,
    video_maxrate,
    video_bufsize,
    video_preset,
    video_profile,
    video_pix_fmt,
    audio_codec,
    audio_channels,
    audio_sample_rate,
  } = values;

  const fps = video_fps && video_fps > 0 ? video_fps : 25;
  const videoGop =
    video_max_keyframe && video_max_keyframe > 0 ? Math.max(1, Math.round(video_max_keyframe / fps)) : undefined;

  let resolvedName = name;
  if (resolution_preset === "original") {
    resolvedName = "original";
  } else if (resolution_preset === "custom") {
    resolvedName = "custom";
  } else if (resolution_preset) {
    resolvedName = RESOLUTION_OPTIONS.find((o) => o.value === resolution_preset)?.name ?? name;
  }

  const resolvedHeight =
    resolution_preset === "custom" ? height : RESOLUTION_OPTIONS.find((o) => o.value === resolution_preset)?.height ?? height;

  return {
    rendition: {
      name: resolvedName,
      height: resolvedHeight,
      width: resolution_preset === "custom" ? width : undefined,
      video_bitrate: bitrate_mode === "crf" ? video_bitrate || "5000k" : video_bitrate,
      audio_bitrate,
      resolution_preset,
      bitrate_mode,
      video_crf: bitrate_mode === "crf" ? video_crf : undefined,
    },
    presetEncoding: {
      video_crf: bitrate_mode === "crf" ? video_crf : 0,
      video_maxrate,
      video_bufsize,
      video_preset,
      video_profile,
      video_gop: videoGop,
      video_pix_fmt,
      encryption_mode,
      audio_codec: (audio_codec as Preset["audio_codec"]) ?? "aac",
      audio_channels,
      audio_sample_rate,
      audio_bitrate: audio_bitrate || "128k",
    },
  };
}

type RenditionConfigModalProps = {
  open: boolean;
  mode: "add" | "edit";
  mainForm: ReturnType<typeof Form.useForm>[0];
  editIndex: number | null;
  onClose: () => void;
  onConfirm: (rendition: RenditionEntry, presetEncoding: Partial<Preset>) => void;
};

function RenditionConfigModal({ open, mode, mainForm, editIndex, onClose, onConfirm }: RenditionConfigModalProps) {
  const t = useT();
  const [form] = Form.useForm<RenditionFormValues>();
  const resolutionPreset = Form.useWatch("resolution_preset", form) as ResolutionPreset | undefined;
  const bitrateMode = Form.useWatch("bitrate_mode", form) as BitrateMode | undefined;
  const mainFormat = mainForm.getFieldValue("output_format") as Preset["output_format"] | undefined;

  useEffect(() => {
    if (!open) return;
    const presetValues = mainForm.getFieldsValue(true) as Partial<Preset>;
    if (mode === "edit" && editIndex !== null) {
      const renditions = (presetValues.renditions ?? []) as RenditionEntry[];
      const r = renditions[editIndex];
      if (r) {
        const bitrate_mode: BitrateMode = r.bitrate_mode ?? ((presetValues.video_crf ?? 0) > 0 ? "crf" : "average");
        form.setFieldsValue({
          ...defaultRenditionFormValues(presetValues),
          ...r,
          resolution_preset: r.resolution_preset ?? guessResolutionPreset(r),
          bitrate_mode,
          video_crf: r.video_crf ?? presetValues.video_crf ?? 23,
          video_max_keyframe: presetValues.video_gop ? presetValues.video_gop * 25 : 250,
          encryption_mode: presetValues.encryption_mode ?? "none",
        });
      }
    } else {
      form.setFieldsValue(defaultRenditionFormValues(presetValues));
    }
  }, [open, mode, editIndex, mainForm, form]);

  const handleResolutionChange = (value: ResolutionPreset) => {
    if (value === "custom") {
      form.setFieldsValue({ resolution_preset: "custom", width: undefined });
      return;
    }
    const opt = RESOLUTION_OPTIONS.find((o) => o.value === value);
    if (!opt) return;
    form.setFieldsValue({
      resolution_preset: value,
      height: opt.height,
      name: opt.name,
      width: undefined,
      video_bitrate: opt.video_bitrate,
      audio_bitrate: opt.audio_bitrate,
    });
  };

  const handleOk = async () => {
    try {
      const values = await form.validateFields();
      const { rendition, presetEncoding } = splitRenditionValues(values);
      onConfirm(rendition, presetEncoding);
      onClose();
    } catch {
      // validation failed
    }
  };

  const title =
    mode === "add"
      ? t("pretranscode.preset.rendition_add_title")
      : t("pretranscode.preset.rendition_edit_title");

  const resolutionSelectOptions = [
    { value: "original", label: t("pretranscode.preset.rendition_original") },
    { value: "custom", label: t("pretranscode.preset.rendition_custom") },
    ...RESOLUTION_OPTIONS.filter((o) => o.value !== "original").map((o) => ({
      value: o.value,
      label: o.name,
    })),
  ];

  return (
    <Modal
      title={title}
      open={open}
      onCancel={onClose}
      onOk={handleOk}
      okText={t("common.confirm")}
      cancelText={t("common.cancel")}
      width={600}
      destroyOnClose
    >
      <Form form={form} layout="vertical" initialValues={{ resolution_preset: "720p", bitrate_mode: "crf" }}>
        <Collapse
          defaultActiveKey={["video", "audio", "other"]}
          items={[
            {
              key: "video",
              label: t("pretranscode.preset.video_settings"),
              children: (
                <>
                  <Row gutter={16} align="bottom">
                    <Col span={resolutionPreset === "custom" ? 10 : 24}>
                      <Form.Item
                        name="resolution_preset"
                        label={t("pretranscode.preset.output_resolution")}
                        rules={[{ required: true }]}
                      >
                        <Select options={resolutionSelectOptions} onChange={handleResolutionChange} />
                      </Form.Item>
                    </Col>
                    {resolutionPreset === "custom" && (
                      <>
                        <Col span={7}>
                          <Form.Item name="width" label={t("pretranscode.preset.rendition_width")}>
                            <InputNumber min={0} style={{ width: "100%" }} placeholder="auto" />
                          </Form.Item>
                        </Col>
                        <Col span={7}>
                          <Form.Item
                            name="height"
                            label={t("pretranscode.preset.rendition_height")}
                            rules={[{ required: true }]}
                          >
                            <InputNumber min={1} style={{ width: "100%" }} />
                          </Form.Item>
                        </Col>
                      </>
                    )}
                  </Row>

                  <Form.Item
                    name="bitrate_mode"
                    label={t("pretranscode.preset.bitrate_control")}
                    rules={[{ required: true }]}
                  >
                    <Select
                      options={[
                        { value: "average", label: t("pretranscode.preset.bitrate_average") },
                        { value: "crf", label: t("pretranscode.preset.bitrate_crf") },
                      ]}
                    />
                  </Form.Item>

                  {bitrateMode === "average" ? (
                    <Form.Item
                      name="video_bitrate"
                      label={t("pretranscode.preset.rendition_video_bitrate")}
                      rules={[{ required: true }]}
                    >
                      <Input placeholder="5000k" />
                    </Form.Item>
                  ) : (
                    <Form.Item
                      name="video_crf"
                      label={t("pretranscode.preset.video_crf")}
                      rules={[{ required: true }]}
                    >
                      <InputNumber min={0} max={51} style={{ width: "100%" }} />
                    </Form.Item>
                  )}

                  <Row gutter={16}>
                    <Col span={12}>
                      <Form.Item name="video_max_keyframe" label={t("pretranscode.preset.video_max_keyframe")}>
                        <InputNumber min={1} style={{ width: "100%" }} placeholder="250" />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item name="video_fps" label={t("pretranscode.preset.video_fps")}>
                        <InputNumber min={1} max={120} style={{ width: "100%" }} placeholder="25" />
                      </Form.Item>
                    </Col>
                  </Row>

                  <Row gutter={16}>
                    <Col span={8}>
                      <Form.Item name="video_preset" label={t("pretranscode.preset.video_preset")}>
                        <Select options={X264_PRESETS.map((v) => ({ value: v, label: v }))} allowClear />
                      </Form.Item>
                    </Col>
                    <Col span={8}>
                      <Form.Item name="video_profile" label={t("pretranscode.preset.video_profile")}>
                        <Select options={PROFILES.map((v) => ({ value: v, label: v }))} allowClear />
                      </Form.Item>
                    </Col>
                    <Col span={8}>
                      <Form.Item name="video_maxrate" label={t("pretranscode.preset.video_maxrate")}>
                        <Input placeholder="5000k" />
                      </Form.Item>
                    </Col>
                  </Row>
                  <Row gutter={16}>
                    <Col span={12}>
                      <Form.Item name="video_bufsize" label={t("pretranscode.preset.video_bufsize")}>
                        <Input placeholder="10M" />
                      </Form.Item>
                    </Col>
                    <Col span={12}>
                      <Form.Item name="video_pix_fmt" label={t("pretranscode.preset.video_pix_fmt")}>
                        <Input placeholder="yuv420p" />
                      </Form.Item>
                    </Col>
                  </Row>
                </>
              ),
            },
            {
              key: "audio",
              label: t("pretranscode.preset.audio_settings"),
              children: (
                <Row gutter={16}>
                  <Col span={12}>
                    <Form.Item name="audio_codec" label={t("pretranscode.preset.audio_codec")} rules={[{ required: true }]}>
                      <Select options={AUDIO_CODECS.map((v) => ({ value: v, label: v }))} />
                    </Form.Item>
                  </Col>
                  <Col span={12}>
                    <Form.Item name="audio_bitrate" label={t("pretranscode.preset.rendition_audio_bitrate")}>
                      <Input placeholder="128k" />
                    </Form.Item>
                  </Col>
                  <Col span={12}>
                    <Form.Item name="audio_channels" label={t("pretranscode.preset.audio_channels")}>
                      <InputNumber min={1} max={8} style={{ width: "100%" }} />
                    </Form.Item>
                  </Col>
                  <Col span={12}>
                    <Form.Item name="audio_sample_rate" label={t("pretranscode.preset.audio_sample_rate")}>
                      <InputNumber min={8000} max={192000} style={{ width: "100%" }} />
                    </Form.Item>
                  </Col>
                </Row>
              ),
            },
            {
              key: "other",
              label: t("pretranscode.preset.other_settings"),
              children: (
                <Form.Item name="encryption_mode" label={t("pretranscode.preset.encryption")}>
                  <Select
                    options={ENCRYPTIONS.map((e) => ({
                      value: e.value,
                      label: t(`pretranscode.preset.${e.value}`),
                    }))}
                    disabled={mainFormat === "mp4" || mainFormat === "flv"}
                  />
                </Form.Item>
              ),
            },
          ]}
        />
        <Text type="secondary" style={{ fontSize: 12, display: "block", marginTop: 8 }}>
          {t("pretranscode.preset.encoding_shared_hint")}
        </Text>
      </Form>
    </Modal>
  );
}

export function PresetTab() {
  const t = useT();
  const [presets, setPresets] = useState<Preset[]>([]);
  const [loading, setLoading] = useState(true);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<Preset | null>(null);
  const [renditionModalOpen, setRenditionModalOpen] = useState(false);
  const [renditionModalMode, setRenditionModalMode] = useState<"add" | "edit">("add");
  const [renditionEditIndex, setRenditionEditIndex] = useState<number | null>(null);
  const [renditions, setRenditions] = useState<RenditionEntry[]>([]);
  const [encoders, setEncoders] = useState<EncoderInfo[]>([]);
  const [form] = Form.useForm();
  const outputDirMode = Form.useWatch("output_dir_mode", form) as Preset["output_dir_mode"] | undefined;

  const load = async () => {
    setLoading(true);
    try {
      const [items, enc] = await Promise.all([fetchPresets(), fetchAvailableEncoders()]);
      setPresets(items);
      setEncoders(enc);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    const defaults = buildDefaultRenditions("hls");
    setRenditions(defaults);
    form.setFieldsValue({
      output_format: "hls",
      encryption_mode: "none",
      video_codec: "libx264",
      video_preset: "veryfast",
      video_crf: 23,
      audio_codec: "aac",
      audio_bitrate: "128k",
      audio_channels: 2,
      audio_sample_rate: 48000,
      hw_fallback: true,
      output_dir_mode: "source",
      output_dir_custom: "",
      renditions: defaults,
    });
    setDrawerOpen(true);
  };

  // When creating (not editing), update default renditions on format change.
  const handleFormatChange = (val: Preset["output_format"]) => {
    if (editing) return; // don't touch existing presets' renditions
    const next = buildDefaultRenditions(val);
    setRenditions(next);
    form.setFieldsValue({ renditions: next });
  };

  const openEdit = (p: Preset) => {
    setEditing(p);
    const items = (p.renditions ?? []).map((r) => enrichRenditionEntry(r, p));
    setRenditions(items);
    form.setFieldsValue({
      ...p,
      renditions: items,
    });
    setDrawerOpen(true);
  };

  const openAddRendition = () => {
    setRenditionModalMode("add");
    setRenditionEditIndex(null);
    setRenditionModalOpen(true);
  };

  const openEditRendition = (index: number) => {
    setRenditionModalMode("edit");
    setRenditionEditIndex(index);
    setRenditionModalOpen(true);
  };

  const handleRenditionConfirm = (rendition: RenditionEntry, presetEncoding: Partial<Preset>) => {
    let next: RenditionEntry[];
    if (renditionModalMode === "add") {
      next = [...renditions, rendition];
    } else if (renditionEditIndex !== null) {
      next = renditions.map((r, i) => (i === renditionEditIndex ? rendition : r));
    } else {
      next = renditions;
    }
    setRenditions(next);
    form.setFieldsValue({ renditions: next, ...presetEncoding });
  };

  const removeRendition = (index: number) => {
    const next = renditions.filter((_, i) => i !== index);
    setRenditions(next);
    form.setFieldsValue({ renditions: next });
  };

  const submit = async () => {
    const values = await form.validateFields();
    values.renditions = renditions;
    if (values.output_format === "mp4" || values.output_format === "flv") {
      values.encryption_mode = "none";
    }
    if (!values.renditions?.length) {
      message.warning(t("pretranscode.preset.rendition_required"));
      return;
    }
    if (values.output_dir_mode === "custom" && !String(values.output_dir_custom ?? "").trim()) {
      message.warning(t("pretranscode.preset.output_dir_custom_required"));
      return;
    }
    if (!values.audio_bitrate) {
      values.audio_bitrate = values.renditions[0]?.audio_bitrate || "128k";
    }
    try {
      if (editing) {
        await updatePreset(editing.id, values);
        message.success(t("common.saved"));
      } else {
        await createPreset(values);
        message.success(t("common.saved"));
      }
      setDrawerOpen(false);
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onClone = async (p: Preset) => {
    try {
      await clonePreset(p.id, `${p.name} (copy)`);
      message.success(t("common.saved"));
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onDelete = async (p: Preset) => {
    try {
      await deletePreset(p.id);
      message.success(t("common.delete_success"));
      load();
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const onToggle = async (p: Preset, checked: boolean) => {
    try {
      await togglePreset(p.id);
      setPresets((prev) => prev.map((x) => (x.id === p.id ? { ...x, is_enabled: checked } : x)));
    } catch (e) {
      message.error((e as Error).message);
    }
  };

  const columns = [
    {
      title: t("pretranscode.preset.name"),
      dataIndex: "name",
      render: (name: string, r: Preset) => (
        <Space>
          <strong>{name}</strong>
          {r.is_builtin && <Tag color="blue">{t("pretranscode.preset.builtin")}</Tag>}
          {r.encryption_mode !== "none" && <Tag color="gold">{r.encryption_mode}</Tag>}
        </Space>
      ),
    },
    { title: t("pretranscode.preset.output_format"), dataIndex: "output_format", width: 100 },
    {
      title: t("pretranscode.preset.renditions"),
      dataIndex: "renditions",
      render: (items: Rendition[]) => (items ?? []).map((r) => r.name).join(", ") || "-",
    },
    {
      title: t("pretranscode.preset.video_codec"),
      dataIndex: "video_codec",
      width: 120,
    },
    {
      title: t("pretranscode.preset.enabled"),
      key: "enabled",
      width: 80,
      render: (_: unknown, r: Preset) => (
        <Switch checked={r.is_enabled} onChange={(c) => onToggle(r, c)} size="small" />
      ),
    },
    {
      title: "",
      key: "actions",
      width: 160,
      render: (_: unknown, r: Preset) => (
        <Space size="small">
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(r)} />
          <Button size="small" icon={<CopyOutlined />} onClick={() => onClone(r)} />
          {!r.is_builtin && (
            <Popconfirm title={t("pretranscode.preset.delete_confirm")} onConfirm={() => onDelete(r)}>
              <Button size="small" danger icon={<DeleteOutlined />} />
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  return (
    <Card
      title={t("pretranscode.preset.title")}
      extra={
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          {t("pretranscode.preset.create")}
        </Button>
      }
    >
      <Table rowKey="id" loading={loading} dataSource={presets} columns={columns} pagination={{ pageSize: 10 }} />
      <Drawer
        title={editing ? t("pretranscode.preset.edit") : t("pretranscode.preset.create")}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        width={720}
        extra={
          <Space>
            <Button onClick={() => setDrawerOpen(false)}>{t("common.cancel")}</Button>
            <Button type="primary" onClick={submit}>
              {t("common.save")}
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t("pretranscode.preset.name")} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="description" label={t("pretranscode.preset.description")}>
            <Input.TextArea rows={2} />
          </Form.Item>

          <Row gutter={16}>
            <Col span={12}>
              <Form.Item name="output_dir_mode" label={t("pretranscode.preset.output_dir_mode")}>
                <Select
                  options={OUTPUT_DIR_MODES.map((m) => ({ value: m.value, label: t(m.labelKey) }))}
                />
              </Form.Item>
            </Col>
            {outputDirMode === "custom" && (
              <Col span={12}>
                <Form.Item
                  name="output_dir_custom"
                  label={t("pretranscode.preset.output_dir_custom")}
                  rules={[{ required: true, whitespace: true, message: t("pretranscode.preset.output_dir_custom_required") }]}
                >
                  <Input placeholder={t("pretranscode.preset.output_dir_custom_placeholder")} />
                </Form.Item>
              </Col>
            )}
          </Row>
          {outputDirMode === "source" && (
            <Typography.Paragraph type="secondary" style={{ marginTop: -8, marginBottom: 16, fontSize: 13 }}>
              {t("pretranscode.preset.output_dir_source_hint")}
            </Typography.Paragraph>
          )}

          <Row gutter={16}>
            <Col span={12}>
              <Form.Item name="output_format" label={t("pretranscode.preset.output_format")} rules={[{ required: true }]}>
                <Select options={FORMATS} onChange={handleFormatChange} />
              </Form.Item>
            </Col>
            <Col span={12}>
              <Form.Item name="video_codec" label={t("pretranscode.preset.video_codec")} rules={[{ required: true }]}>
                <Select options={encoderOptions(encoders)} />
              </Form.Item>
            </Col>
          </Row>

          <div style={{ marginBottom: 16 }}>
            <Text strong style={{ display: "block", marginBottom: 8 }}>
              {t("pretranscode.preset.renditions")}
            </Text>
            <div style={{ border: "1px solid #303030", borderRadius: 8, overflow: "hidden" }}>
              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: "1fr 1fr 88px",
                  gap: 8,
                  padding: "8px 12px",
                  background: "#141414",
                  borderBottom: "1px solid #303030",
                  fontSize: 12,
                  color: "#888",
                  textTransform: "uppercase",
                }}
              >
                <span>{t("pretranscode.preset.rendition_size")}</span>
                <span>{t("pretranscode.preset.rendition_quality")}</span>
                <span />
              </div>
              {renditions.length === 0 ? (
                <div style={{ padding: "16px 12px", color: "#666", fontSize: 13 }}>
                  {t("pretranscode.preset.no_renditions")}
                </div>
              ) : (
                renditions.map((r, index) => (
                  <div
                    key={`${r.height}-${r.name}-${index}`}
                    style={{
                      display: "grid",
                      gridTemplateColumns: "1fr 1fr 88px",
                      gap: 8,
                      padding: "10px 12px",
                      alignItems: "center",
                      borderBottom: "1px solid #1f1f1f",
                    }}
                  >
                    <Text>{renditionSizeLabel(t, r)}</Text>
                    <Text>{renditionQualityLabel(r)}</Text>
                    <Space size={4}>
                      <Tooltip title={t("pretranscode.preset.rendition_config")}>
                        <Button
                          type="text"
                          size="small"
                          icon={<SettingOutlined />}
                          onClick={() => openEditRendition(index)}
                        />
                      </Tooltip>
                      <Tooltip title={renditions.length <= 1 ? t("pretranscode.preset.rendition_required") : t("common.delete")}>
                        <Button
                          type="text"
                          size="small"
                          danger
                          disabled={renditions.length <= 1}
                          icon={<DeleteOutlined />}
                          onClick={() => removeRendition(index)}
                        />
                      </Tooltip>
                    </Space>
                  </div>
                ))
              )}
            </div>
            <Button type="link" icon={<PlusOutlined />} style={{ paddingLeft: 0, marginTop: 8 }} onClick={openAddRendition}>
              {t("pretranscode.preset.rendition_add_size")}
            </Button>
          </div>
        </Form>
      </Drawer>

      <RenditionConfigModal
        open={renditionModalOpen}
        mode={renditionModalMode}
        mainForm={form}
        editIndex={renditionEditIndex}
        onClose={() => setRenditionModalOpen(false)}
        onConfirm={handleRenditionConfirm}
      />
    </Card>
  );
}
