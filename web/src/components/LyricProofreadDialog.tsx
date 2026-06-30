import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Empty,
  Input,
  message,
  Modal,
  Space,
  Spin,
  Upload,
  type UploadProps,
} from "antd";
import { UploadOutlined } from "@ant-design/icons";
import {
  fetchMediaLyrics,
  importMediaLyrics,
  saveMediaLyrics,
} from "../api/client";
import { useProofreadDialogStore } from "../store/proofreadDialog";
import { tGlobal as t } from "../i18n";

const { TextArea } = Input;

type LrcLine = {
  prefix: string; // leading [mm:ss.xx] tags
  text: string;
  raw: string; // original full line (for non-lyric lines)
  isLyric: boolean;
};

const lrcTimestampRegex = /^\[\d{1,2}:\d{1,2}\.\d{1,3}\]/;

function parseLrc(content: string): LrcLine[] {
  const lines = content.replace(/\r\n/g, "\n").replace(/\r/g, "\n").split("\n");
  return lines.map((raw) => {
    const trimmed = raw.trim();
    if (trimmed === "") {
      return { prefix: "", text: "", raw, isLyric: false };
    }
    let rest = trimmed;
    let prefix = "";
    while (rest.startsWith("[")) {
      const end = rest.indexOf("]");
      if (end < 0) break;
      const tag = rest.slice(0, end + 1);
      if (!lrcTimestampRegex.test(tag)) break;
      prefix += tag;
      rest = rest.slice(end + 1);
    }
    if (!prefix) {
      return { prefix: "", text: "", raw, isLyric: false };
    }
    return { prefix, text: rest.trim(), raw, isLyric: true };
  });
}

function renderLrc(lines: LrcLine[]): string {
  return lines
    .map((l) => (l.isLyric ? `${l.prefix}${l.text}` : l.raw))
    .join("\n")
    .trim();
}

export function LyricProofreadDialog() {
  const target = useProofreadDialogStore((s) => s.lyric);
  const close = useProofreadDialogStore((s) => s.closeLyric);
  const mediaId = target?.mediaId ?? 0;

  const [lines, setLines] = useState<LrcLine[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const lyricIndices = useMemo(
    () => lines.map((l, i) => (l.isLyric ? i : -1)).filter((i) => i >= 0),
    [lines],
  );

  useEffect(() => {
    if (!target) {
      setLines([]);
      setSelected(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setLines([]);
    setSelected(null);
    void fetchMediaLyrics(mediaId)
      .then((res) => {
        if (cancelled) return;
        const parsed = parseLrc(res.lrc || "");
        setLines(parsed);
        const first = parsed.findIndex((l) => l.isLyric);
        setSelected(first >= 0 ? first : null);
      })
      .catch(() => {
        if (!cancelled) message.error(t("proofread.load_failed"));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [target, mediaId]);

  const updateText = (idx: number, text: string) => {
    setLines((prev) => prev.map((l, i) => (i === idx ? { ...l, text } : l)));
  };

  const handleSave = async () => {
    if (lines.length === 0) return;
    setSaving(true);
    try {
      await saveMediaLyrics(mediaId, renderLrc(lines));
      message.success(t("proofread.saved"));
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      message.error(msg || t("proofread.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const importProps: UploadProps = {
    accept: ".lrc,.vtt",
    showUploadList: false,
    beforeUpload: (file) => {
      void (async () => {
        try {
          await importMediaLyrics(mediaId, file);
          message.success(t("proofread.imported"));
          const res = await fetchMediaLyrics(mediaId);
          const parsed = parseLrc(res.lrc || "");
          setLines(parsed);
          const first = parsed.findIndex((l) => l.isLyric);
          setSelected(first >= 0 ? first : null);
        } catch (err: unknown) {
          const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
          message.error(msg || t("proofread.import_failed"));
        }
      })();
      return false;
    },
  };

  const selectedLine = selected != null ? lines[selected] : null;
  const selectedLyricOrdinal =
    selected != null ? lyricIndices.indexOf(selected) + 1 : 0;

  return (
    <Modal
      title={t("proofread.lyric_title")}
      open={!!target}
      onCancel={close}
      width={820}
      centered
      destroyOnClose
      footer={
        <Space>
          <Upload {...importProps}>
            <Button icon={<UploadOutlined />}>{t("proofread.import_lyric")}</Button>
          </Upload>
          <Button onClick={close}>{t("proofread.cancel")}</Button>
          <Button type="primary" loading={saving} onClick={handleSave} disabled={lyricIndices.length === 0}>
            {t("proofread.save")}
          </Button>
        </Space>
      }
    >
      <Spin spinning={loading}>
        {lyricIndices.length === 0 && !loading ? (
          <Empty description={t("proofread.no_lyrics")} />
        ) : (
          <div style={{ display: "flex", gap: 12, height: 440 }}>
            <div
              style={{
                flex: "1 1 55%",
                overflow: "auto",
                border: "1px solid #303030",
                borderRadius: 6,
              }}
            >
              {lines.map((l, i) =>
                l.isLyric ? (
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
                    <span style={{ fontSize: 11, color: "#8c8c8c", fontFamily: "monospace", marginRight: 8 }}>
                      {l.prefix}
                    </span>
                    <span style={{ whiteSpace: "pre-wrap", wordBreak: "break-word" }}>{l.text || " "}</span>
                  </div>
                ) : null,
              )}
            </div>
            <div style={{ flex: "1 1 45%", display: "flex", flexDirection: "column" }}>
              <div style={{ marginBottom: 6, color: "#8c8c8c", fontSize: 12 }}>
                {selectedLyricOrdinal > 0
                  ? `${t("proofread.line")} ${selectedLyricOrdinal} / ${lyricIndices.length}`
                  : ""}
              </div>
              {selectedLine && selectedLine.isLyric ? (
                <>
                  <div style={{ fontSize: 11, color: "#8c8c8c", fontFamily: "monospace", marginBottom: 6 }}>
                    {selectedLine.prefix}
                  </div>
                  <TextArea
                    value={selectedLine.text}
                    onChange={(e) => updateText(selected!, e.target.value)}
                    autoSize={{ minRows: 6, maxRows: 16 }}
                    placeholder={t("proofread.edit_placeholder")}
                  />
                  <Space style={{ marginTop: 8 }}>
                    <Button
                      size="small"
                      disabled={selectedLyricOrdinal <= 1}
                      onClick={() => {
                        const prev = lyricIndices[selectedLyricOrdinal - 2];
                        if (prev != null) setSelected(prev);
                      }}
                    >
                      {t("proofread.prev")}
                    </Button>
                    <Button
                      size="small"
                      disabled={selectedLyricOrdinal >= lyricIndices.length}
                      onClick={() => {
                        const next = lyricIndices[selectedLyricOrdinal];
                        if (next != null) setSelected(next);
                      }}
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
        )}
      </Spin>
    </Modal>
  );
}
