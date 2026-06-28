import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Empty,
  Input,
  message,
  Modal,
  Space,
  Spin,
  Tabs,
  Tag,
  Upload,
  type UploadProps,
} from "antd";
import { UploadOutlined } from "@ant-design/icons";
import {
  fetchMediaSubtitles,
  fetchSubtitleCues,
  importSubtitle,
  saveSubtitleCues,
  type MediaSubtitleRow,
  type SubtitleCue,
} from "../api/client";
import { useProofreadDialogStore } from "../store/proofreadDialog";
import { tGlobal as t } from "../i18n";

const { TextArea } = Input;

function langLabel(sub: MediaSubtitleRow): string {
  const lang = (sub.lang || "und").trim();
  const label = (sub.label || "").trim();
  const kind = (sub.source_kind || "").trim();
  const parts: string[] = [];
  parts.push(label || langDisplayName(lang));
  if (kind) parts.push(kindLabel(kind));
  if (label && lang && lang !== "und") parts.push(lang);
  return parts.join(" · ");
}

function langDisplayName(lang: string): string {
  const map: Record<string, string> = {
    und: "未知语言",
    zh: "中文",
    chi: "中文",
    "zh-CN": "简体中文",
    "zh-TW": "繁体中文",
    en: "English",
    eng: "English",
    ja: "日本語",
    jpn: "日本語",
    ko: "한국어",
    kor: "한국어",
  };
  return map[lang] || lang;
}

function kindLabel(kind: string): string {
  const map: Record<string, string> = {
    embedded: "内嵌",
    external: "外挂",
    embedded_ocr: "内嵌图形(OCR)",
    external_ocr: "外挂图形(OCR)",
    asr: "语音识别",
    imported: "导入",
  };
  return map[kind] || kind;
}

export function SubtitleProofreadDialog() {
  const target = useProofreadDialogStore((s) => s.subtitle);
  const close = useProofreadDialogStore((s) => s.closeSubtitle);
  const mediaId = target?.mediaId ?? 0;

  const [subs, setSubs] = useState<MediaSubtitleRow[]>([]);
  const [activeSubId, setActiveSubId] = useState<number | null>(null);
  const [cues, setCues] = useState<SubtitleCue[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [loadingSubs, setLoadingSubs] = useState(false);
  const [loadingCues, setLoadingCues] = useState(false);
  const [saving, setSaving] = useState(false);

  const readySubs = useMemo(() => subs.filter((s) => s.status === "ready"), [subs]);

  // Load subtitle list when dialog opens.
  useEffect(() => {
    if (!target) {
      setSubs([]);
      setActiveSubId(null);
      setCues([]);
      setSelected(null);
      return;
    }
    let cancelled = false;
    setLoadingSubs(true);
    void fetchMediaSubtitles(mediaId)
      .then((rows) => {
        if (cancelled) return;
        setSubs(rows);
        const ready = rows.filter((r) => r.status === "ready");
        setActiveSubId(ready.length > 0 ? ready[0].id : null);
      })
      .catch(() => {
        if (!cancelled) message.error(t("proofread.load_failed"));
      })
      .finally(() => {
        if (!cancelled) setLoadingSubs(false);
      });
    return () => {
      cancelled = true;
    };
  }, [target, mediaId]);

  // Load cues when active subtitle changes.
  useEffect(() => {
    if (!target || activeSubId == null) {
      setCues([]);
      setSelected(null);
      return;
    }
    let cancelled = false;
    setLoadingCues(true);
    setCues([]);
    setSelected(null);
    void fetchSubtitleCues(mediaId, activeSubId)
      .then((res) => {
        if (cancelled) return;
        setCues(res.cues ?? []);
        setSelected(res.cues && res.cues.length > 0 ? 0 : null);
      })
      .catch(() => {
        if (!cancelled) message.error(t("proofread.load_failed"));
      })
      .finally(() => {
        if (!cancelled) setLoadingCues(false);
      });
    return () => {
      cancelled = true;
    };
  }, [target, mediaId, activeSubId]);

  const updateCueText = (idx: number, text: string) => {
    setCues((prev) => prev.map((c, i) => (i === idx ? { ...c, text } : c)));
  };

  const handleSave = async () => {
    if (activeSubId == null || cues.length === 0) return;
    setSaving(true);
    try {
      await saveSubtitleCues(mediaId, activeSubId, cues);
      message.success(t("proofread.saved"));
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      message.error(msg || t("proofread.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const importProps: UploadProps = {
    accept: ".vtt,.srt,.ass,.ssa",
    showUploadList: false,
    beforeUpload: (file) => {
      void (async () => {
        try {
          const row = await importSubtitle(mediaId, file);
          message.success(t("proofread.imported"));
          const rows = await fetchMediaSubtitles(mediaId);
          setSubs(rows);
          setActiveSubId(row.id);
        } catch (err: unknown) {
          const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
          message.error(msg || t("proofread.import_failed"));
        }
      })();
      return false; // prevent auto-upload
    },
  };

  const selectedCue = selected != null ? cues[selected] : null;

  return (
    <Modal
      title={t("proofread.subtitle_title")}
      open={!!target}
      onCancel={close}
      width={860}
      centered
      destroyOnClose
      footer={
        <Space>
          <Upload {...importProps}>
            <Button icon={<UploadOutlined />}>{t("proofread.import_subtitle")}</Button>
          </Upload>
          <Button onClick={close}>{t("proofread.cancel")}</Button>
          <Button type="primary" loading={saving} onClick={handleSave} disabled={cues.length === 0}>
            {t("proofread.save")}
          </Button>
        </Space>
      }
    >
      <Spin spinning={loadingSubs}>
        {readySubs.length === 0 ? (
          <Empty description={t("proofread.no_subtitles")} />
        ) : (
          <>
            <Tabs
              size="small"
              activeKey={activeSubId != null ? String(activeSubId) : undefined}
              onChange={(k) => setActiveSubId(Number(k))}
              items={readySubs.map((s) => ({
                key: String(s.id),
                label: (
                  <span>
                    {langLabel(s)}
                    {s.source_kind === "imported" ? <Tag color="blue" style={{ marginLeft: 6 }}>导入</Tag> : null}
                  </span>
                ),
              }))}
            />
            <Spin spinning={loadingCues}>
              <div style={{ display: "flex", gap: 12, height: 420 }}>
                <div
                  style={{
                    flex: "1 1 55%",
                    overflow: "auto",
                    border: "1px solid #303030",
                    borderRadius: 6,
                  }}
                >
                  {cues.length === 0 ? (
                    <Empty description={t("proofread.no_cues")} style={{ marginTop: 80 }} />
                  ) : (
                    cues.map((c, i) => (
                      <div
                        key={i}
                        onClick={() => setSelected(i)}
                        style={{
                          padding: "6px 10px",
                          cursor: "pointer",
                          borderBottom: "1px solid #262626",
                          background: selected === i ? "#1677ff22" : "transparent",
                        }}
                      >
                        <div style={{ fontSize: 11, color: "#8c8c8c", fontFamily: "monospace" }}>
                          {c.start} → {c.end}
                        </div>
                        <div style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{c.text || " "}</div>
                      </div>
                    ))
                  )}
                </div>
                <div style={{ flex: "1 1 45%", display: "flex", flexDirection: "column" }}>
                  <div style={{ marginBottom: 6, color: "#8c8c8c", fontSize: 12 }}>
                    {selected != null ? `${t("proofread.line")} ${selected + 1} / ${cues.length}` : ""}
                  </div>
                  {selectedCue ? (
                    <>
                      <div style={{ fontSize: 11, color: "#8c8c8c", fontFamily: "monospace", marginBottom: 6 }}>
                        {selectedCue.start} → {selectedCue.end}
                      </div>
                      <TextArea
                        value={selectedCue.text}
                        onChange={(e) => updateCueText(selected!, e.target.value)}
                        autoSize={{ minRows: 6, maxRows: 16 }}
                        placeholder={t("proofread.edit_placeholder")}
                      />
                      <Space style={{ marginTop: 8 }}>
                        <Button
                          size="small"
                          disabled={selected === 0}
                          onClick={() => setSelected((s) => (s != null ? s - 1 : null))}
                        >
                          {t("proofread.prev")}
                        </Button>
                        <Button
                          size="small"
                          disabled={selected === cues.length - 1}
                          onClick={() => setSelected((s) => (s != null ? s + 1 : null))}
                        >
                          {t("proofread.next")}
                        </Button>
                      </Space>
                    </>
                  ) : (
                    <Empty description={t("proofread.select_line")} style={{ marginTop: 80 }} />
                  )}
                </div>
              </div>
            </Spin>
          </>
        )}
      </Spin>
    </Modal>
  );
}
