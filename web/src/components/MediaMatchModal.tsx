import { Button, Empty, Input, InputNumber, Modal, Select, Spin, Typography, message } from "antd";
import { DownOutlined } from "@ant-design/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type MediaItem,
  type MediaMatchListUpdate,
  type ScrapeMatchCandidate,
  manualMatchMedia,
  mediaMatchListUpdate,
  parseScrapeTitle,
  searchScrapeMatches,
} from "../api/client";
import { proxyImageSrc } from "../lib/imageUrl";
import { METADATA_PROVIDER_OPTIONS, providerLabel } from "../lib/scrapeProviders";
import { useT, type TranslateFn } from "../i18n";
import styles from "./MediaMatchModal.module.css";

const { Text, Paragraph } = Typography;

const MATCH_SOURCE_OPTIONS = METADATA_PROVIDER_OPTIONS.filter(
  (o) => o.value !== "fanart" && o.value !== "ai",
);

function buildLanguageOptions(t: TranslateFn): { value: string; label: string }[] {
  return [
    { value: "zh-CN", label: t("components.media_match_modal.lang_zh") },
    { value: "en-US", label: "English" },
    { value: "ja-JP", label: "日本語" },
  ];
}

function parseYearFromTitle(title: string): number | undefined {
  const m = title.match(/(19|20)\d{2}/);
  if (!m) return undefined;
  const y = Number(m[0]);
  return y > 0 ? y : undefined;
}

function defaultMatchSource(title: string): string {
  return /[\u4e00-\u9fff]/.test(title) ? "douban" : "tmdb";
}

function mediaSearchRaw(media: Pick<MediaItem, "title" | "file_path">): string {
  const title = (media.title ?? "").trim();
  if (title) return title;
  const path = (media.file_path ?? "").trim();
  if (!path) return "";
  const base = path.replace(/^.*[/\\]/, "");
  const dot = base.lastIndexOf(".");
  return dot > 0 ? base.slice(0, dot) : base;
}

export interface MediaMatchModalProps {
  media: Pick<MediaItem, "id" | "title" | "year" | "file_path"> | null;
  open: boolean;
  /** True when correcting an existing scrape (修改匹配). */
  fixMatch?: boolean;
  onClose: () => void;
  onMatched?: (update: MediaMatchListUpdate) => void;
}

export default function MediaMatchModal({
  media,
  open,
  fixMatch = false,
  onClose,
  onMatched,
}: MediaMatchModalProps) {
  const t = useT();
  const LANGUAGE_OPTIONS = useMemo(() => buildLanguageOptions(t), [t]);
  const [query, setQuery] = useState("");
  const [year, setYear] = useState<number | null>(null);
  const [source, setSource] = useState("tmdb");
  const [language, setLanguage] = useState("zh-CN");
  const [showSearchOptions, setShowSearchOptions] = useState(false);
  const [searching, setSearching] = useState(false);
  const [matchingId, setMatchingId] = useState<string | null>(null);
  const [results, setResults] = useState<ScrapeMatchCandidate[]>([]);
  const [searchMessage, setSearchMessage] = useState("");
  const [brokenPosters, setBrokenPosters] = useState<Set<string>>(() => new Set());
  const openTokenRef = useRef(0);

  const resultKey = (item: ScrapeMatchCandidate) =>
    `${item.source}:${item.external_id}:${item.media_type ?? ""}`;

  const canSearch = useMemo(() => query.trim().length > 0, [query]);

  const runSearch = useCallback(
    async (params: { query: string; year: number | null; source: string; language: string }) => {
      if (!media || !params.query.trim()) return;
      setSearching(true);
      setSearchMessage("");
      try {
        const data = await searchScrapeMatches({
          query: params.query.trim(),
          year: params.year ?? undefined,
          source: params.source,
          language: params.language,
        });
        setResults(data.items ?? []);
        setBrokenPosters(new Set());
        if ((data.items ?? []).length === 0) {
          setSearchMessage(data.message || t("components.media_match_modal.no_similar"));
        }
      } catch (e: unknown) {
        setResults([]);
        setSearchMessage((e as Error).message || t("components.media_match_modal.search_failed"));
      } finally {
        setSearching(false);
      }
    },
    [media, t],
  );

  useEffect(() => {
    if (!open || !media) return;
    const token = ++openTokenRef.current;
    const rawInput = mediaSearchRaw(media);
    setShowSearchOptions(false);
    setResults([]);
    setSearchMessage("");
    setMatchingId(null);
    setBrokenPosters(new Set());
    setLanguage("zh-CN");

    void (async () => {
      let nextQuery = rawInput;
      let nextYear = (media.year ?? 0) > 0 ? media.year! : null;
      if (rawInput) {
        try {
          const parsed = await parseScrapeTitle(rawInput);
          if (openTokenRef.current !== token) return;
          if (parsed.title) nextQuery = parsed.title;
          if ((nextYear ?? 0) <= 0 && parsed.year) nextYear = parsed.year;
        } catch {
          if (openTokenRef.current !== token) return;
          if ((nextYear ?? 0) <= 0) nextYear = parseYearFromTitle(rawInput) ?? null;
        }
      }
      const nextSource = defaultMatchSource(nextQuery);
      setQuery(nextQuery);
      setYear(nextYear);
      setSource(nextSource);

      if (!nextQuery.trim()) return;
      setSearching(true);
      setSearchMessage("");
      try {
        const data = await searchScrapeMatches({
          query: nextQuery,
          year: nextYear ?? undefined,
          source: nextSource,
          language: "zh-CN",
        });
        if (openTokenRef.current !== token) return;
        setResults(data.items ?? []);
        if ((data.items ?? []).length === 0) {
          setSearchMessage(data.message || t("components.media_match_modal.no_similar"));
        }
      } catch (e: unknown) {
        if (openTokenRef.current !== token) return;
        setResults([]);
        setSearchMessage((e as Error).message || t("components.media_match_modal.search_failed"));
      } finally {
        if (openTokenRef.current === token) setSearching(false);
      }
    })();
  }, [open, media]);

  async function handleSearch() {
    await runSearch({ query, year, source, language });
  }

  async function handleSelect(item: ScrapeMatchCandidate) {
    if (!media || matchingId) return;
    const key = resultKey(item);
    setMatchingId(key);
    try {
      const data = await manualMatchMedia(media.id, {
        source: item.source,
        external_id: item.external_id,
        media_type: item.media_type,
        language,
        query: query.trim(),
        year: year ?? undefined,
        poster: item.poster,
        overview: item.overview,
      });
      message.success(t("components.media_match_modal.matched_as", { title: item.title }));
      onMatched?.(mediaMatchListUpdate(media.id, data, item));
      onClose();
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.media_match_modal.match_failed"));
    } finally {
      setMatchingId(null);
    }
  }

  return (
    <Modal
      title={fixMatch ? t("components.media_match_modal.title_fix") : t("components.media_match_modal.title_match")}
      open={open}
      onCancel={onClose}
      width={720}
      destroyOnClose
      centered
      footer={[
        <Button key="cancel" onClick={onClose}>
          {t("components.media_match_modal.cancel")}
        </Button>,
        <Button
          key="search"
          type="primary"
          loading={searching}
          disabled={!canSearch}
          onClick={() => void handleSearch()}
        >
          {t("components.media_match_modal.search")}
        </Button>,
      ]}
    >
      {media?.file_path ? (
        <div className={styles.filePath}>
          <Text type="secondary">{t("components.media_match_modal.label_location")}</Text>
          <Text className={styles.filePathValue}>{media.file_path}</Text>
        </div>
      ) : null}

      <div className={styles.toolbar}>
        <button
          type="button"
          className={`${styles.toolbarAction} ${showSearchOptions ? styles.toolbarActionActive : ""}`}
          onClick={() => setShowSearchOptions((v) => !v)}
        >
          {t("components.media_match_modal.section_search_options")}
        </button>
        <span className={styles.toolbarLabel}>{t("components.media_match_modal.auto_match")}</span>
        <Select
          className={styles.sourceSelect}
          variant="borderless"
          suffixIcon={<DownOutlined className={styles.sourceSelectIcon} />}
          value={source}
          onChange={setSource}
          options={MATCH_SOURCE_OPTIONS.map((o) => ({ value: o.value, label: o.label }))}
          popupMatchSelectWidth={false}
        />
      </div>

      {showSearchOptions ? (
        <div className={styles.searchForm}>
          <div className={styles.formRow}>
            <div className={styles.field}>
              <Text type="secondary">{t("components.media_match_modal.label_title")}</Text>
              <Input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onPressEnter={() => void handleSearch()}
                placeholder={t("components.media_match_modal.title_placeholder")}
              />
            </div>
            <div className={styles.field}>
              <Text type="secondary">{t("components.media_match_modal.label_source")}</Text>
              <Select
                value={source}
                onChange={setSource}
                options={MATCH_SOURCE_OPTIONS.map((o) => ({ value: o.value, label: o.label }))}
                style={{ width: "100%" }}
              />
            </div>
          </div>
          <div className={styles.formRow}>
            <div className={styles.field}>
              <Text type="secondary">{t("components.media_match_modal.label_year")}</Text>
              <InputNumber
                value={year}
                onChange={(v) => setYear(typeof v === "number" ? v : null)}
                min={1800}
                max={2100}
                placeholder={t("components.media_match_modal.year_placeholder")}
                style={{ width: "100%" }}
              />
            </div>
            <div className={styles.field}>
              <Text type="secondary">{t("components.media_match_modal.label_language")}</Text>
              <Select
                value={language}
                onChange={setLanguage}
                options={LANGUAGE_OPTIONS}
                style={{ width: "100%" }}
              />
            </div>
          </div>
        </div>
      ) : null}

      <div className={styles.resultsWrap}>
        {searching ? (
          <div className={styles.centered}>
            <Spin />
          </div>
        ) : results.length === 0 ? (
          <Empty description={searchMessage || t("components.media_match_modal.no_match_results")} image={Empty.PRESENTED_IMAGE_SIMPLE} />
        ) : (
          <ul className={styles.resultList}>
            {results.map((item) => {
              const key = resultKey(item);
              const busy = matchingId === key;
              const disabled = matchingId != null && !busy;
              return (
                <li key={key}>
                  <button
                    type="button"
                    className={styles.resultRow}
                    disabled={disabled}
                    onClick={() => void handleSelect(item)}
                  >
                    <div className={styles.resultMain}>
                      <div className={styles.resultHead}>
                        <span className={styles.resultTitle}>{item.title}</span>
                        {item.year ? <span className={styles.resultYear}>{item.year}</span> : null}
                      </div>
                      <Paragraph className={styles.overview} ellipsis={{ rows: 3 }}>
                        {item.overview || t("components.media_match_modal.no_overview")}
                      </Paragraph>
                      <Text type="secondary" className={styles.sourceTag}>
                        {providerLabel(item.source)}
                      </Text>
                    </div>
                    <div className={styles.resultPosterWrap}>
                      {busy ? (
                        <div className={`${styles.posterPlaceholder} ${styles.posterBusy}`}>
                          <Spin size="small" />
                        </div>
                      ) : item.poster && !brokenPosters.has(key) ? (
                        <img
                          src={proxyImageSrc(item.poster)}
                          alt=""
                          className={styles.poster}
                          loading="lazy"
                          decoding="async"
                          onError={() => {
                            setBrokenPosters((prev) => {
                              if (prev.has(key)) return prev;
                              const next = new Set(prev);
                              next.add(key);
                              return next;
                            });
                          }}
                        />
                      ) : (
                        <div className={styles.posterPlaceholder}>{t("components.media_match_modal.no_image")}</div>
                      )}
                    </div>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Modal>
  );
}
