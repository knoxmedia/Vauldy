import {
  ArrowLeftOutlined,
  FullscreenOutlined,
  MenuOutlined,
  MinusOutlined,
  PlusOutlined,
  SearchOutlined,
  StarFilled,
  StarOutlined,
} from "@ant-design/icons";
import { Button, Input, Select, Space, Spin, message } from "antd";
import { marked } from "marked";
import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
// Legacy build includes Uint8Array.prototype.toHex polyfill required by pdfjs 5.x workers.
import * as pdfjsLib from "pdfjs-dist/legacy/build/pdf.mjs";
import pdfWorkerUrl from "pdfjs-dist/legacy/build/pdf.worker.min.mjs?url";
import ePub from "epubjs";
import {
  addFavorite,
  documentPreviewSrc,
  documentDownloadSrc,
  documentStreamSrc,
  fetchDocumentDetail,
  fetchFavoriteStatus,
  fetchReadProgress,
  isOfficeDocumentFormat,
  removeFavorite,
  saveReadProgress,
} from "../api/client";
import { useAuthStore } from "../store/auth";
import { useT } from "../i18n";
import styles from "./DocumentReader.module.css";

pdfjsLib.GlobalWorkerOptions.workerSrc = pdfWorkerUrl;

type Theme = "light" | "sepia" | "dark";

const READ_PREFS_KEY = "knox.reader.prefs.v1";

function readPrefs(): { theme: Theme; fontSize: number } {
  try {
    const raw = localStorage.getItem(READ_PREFS_KEY);
    if (raw) {
      const p = JSON.parse(raw) as { theme?: Theme; fontSize?: number };
      return { theme: p.theme ?? "light", fontSize: p.fontSize ?? 16 };
    }
  } catch { /* ignore */ }
  return { theme: "light", fontSize: 16 };
}

function savePrefs(theme: Theme, fontSize: number) {
  localStorage.setItem(READ_PREFS_KEY, JSON.stringify({ theme, fontSize }));
}

export default function DocumentReader() {
  const t = useT();
  const { id } = useParams<{ id: string }>();
  const mediaId = Number(id);
  const nav = useNavigate();
  const token = useAuthStore((s) => s.token);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<{ title?: string; format?: string; stream_url?: string } | null>(null);
  const [theme, setTheme] = useState<Theme>(() => readPrefs().theme);
  const [fontSize, setFontSize] = useState(() => readPrefs().fontSize);
  const [tocOpen, setTocOpen] = useState(false);
  const [tocItems, setTocItems] = useState<{ label: string; href: string }[]>([]);
  const [pdfPage, setPdfPage] = useState(1);
  const [pdfPages, setPdfPages] = useState(0);
  const [pdfLoaded, setPdfLoaded] = useState(false);
  const [converting, setConverting] = useState(false);
  const [viewAsPdf, setViewAsPdf] = useState(false);
  const [textContent, setTextContent] = useState("");
  const [searchQ, setSearchQ] = useState("");
  const [epubReady, setEpubReady] = useState(false);
  const [epubAtStart, setEpubAtStart] = useState(true);
  const [epubAtEnd, setEpubAtEnd] = useState(false);
  const [epubPage, setEpubPage] = useState(0);
  const [epubTotalPages, setEpubTotalPages] = useState(0);
  const [favorited, setFavorited] = useState(false);
  const [favoriteBusy, setFavoriteBusy] = useState(false);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const epubRef = useRef<HTMLDivElement>(null);
  const epubBookRef = useRef<ReturnType<typeof ePub> | null>(null);
  const epubRenditionRef = useRef<{
    themes: { fontSize: (size: string) => void };
    next: () => Promise<void>;
    prev: () => Promise<void>;
    display: (target?: string) => Promise<void>;
  } | null>(null);
  const epubResumeRef = useRef("");
  const pdfDocRef = useRef<pdfjsLib.PDFDocumentProxy | null>(null);

  const pdfScale = (fontSize / 16) * 1.5;

  const streamUrl = documentStreamSrc(mediaId, token);

  const persistProgress = useCallback(async (position: string, percent?: number) => {
    localStorage.setItem(`knox.read.${mediaId}`, JSON.stringify({ position, percent }));
    try {
      await saveReadProgress(mediaId, position, percent);
    } catch { /* optional sync */ }
  }, [mediaId]);

  useEffect(() => {
    savePrefs(theme, fontSize);
  }, [theme, fontSize]);

  useEffect(() => {
    if (!mediaId || Number.isNaN(mediaId)) {
      setFavorited(false);
      return;
    }
    let cancelled = false;
    void fetchFavoriteStatus(mediaId)
      .then((v) => {
        if (!cancelled) setFavorited(v);
      })
      .catch(() => {
        if (!cancelled) setFavorited(false);
      });
    return () => {
      cancelled = true;
    };
  }, [mediaId]);

  useEffect(() => {
    if (!mediaId || Number.isNaN(mediaId)) return;
    setLoading(true);
    setError("");
    setPdfLoaded(false);
    setEpubReady(false);
    setEpubAtStart(true);
    setEpubAtEnd(false);
    setEpubPage(0);
    setEpubTotalPages(0);
    pdfDocRef.current?.destroy();
    pdfDocRef.current = null;
    void (async () => {
      try {
        const d = await fetchDocumentDetail(mediaId);
        setDetail(d as typeof detail);
        const saved = localStorage.getItem(`knox.read.${mediaId}`);
        let position = "";
        try {
          const rp = await fetchReadProgress(mediaId);
          position = rp.position;
        } catch { /* ignore */ }
        if (!position && saved) {
          try { position = JSON.parse(saved).position ?? ""; } catch { /* ignore */ }
        }
        const fmt = ((d as { format?: string }).format || "").toLowerCase();
        const officePreview = isOfficeDocumentFormat(fmt) || Boolean((d as { needs_preview?: boolean }).needs_preview);
        setViewAsPdf(fmt === "pdf" || officePreview);
        if (fmt === "pdf" || officePreview) {
          if (officePreview && fmt !== "pdf") {
            setConverting(true);
          }
          const pdfUrl = officePreview && fmt !== "pdf" ? documentPreviewSrc(mediaId, token) : streamUrl;
          await loadPDF(position, pdfUrl);
          setConverting(false);
        } else if (fmt === "epub") {
          epubResumeRef.current = position;
        } else if (["txt", "md", "mdx", "csv", "html", "htm"].includes(fmt)) {
          await loadText(fmt, position);
        } else {
          setError(t("pages.document_reader.unsupported_format", { format: fmt.toUpperCase() }));
        }
      } catch (e) {
        setConverting(false);
        const msg = e instanceof Error ? e.message : String(e);
        setError(
          msg.includes("500") || /conversion|convert|转换/i.test(msg)
            ? t("pages.document_reader.office_convert_failed", { msg })
            : msg || t("pages.document_reader.cannot_open"),
        );
      } finally {
        setLoading(false);
      }
    })();
    return () => {
      epubBookRef.current?.destroy();
      epubBookRef.current = null;
      epubRenditionRef.current = null;
      pdfDocRef.current?.destroy();
    };
  }, [mediaId, token]);

  useEffect(() => {
    const fmt = (detail?.format || "").toLowerCase();
    if (!mediaId || loading || converting || error || fmt !== "epub") return;

    const el = epubRef.current;
    if (!el) return;

    let disposed = false;
    const headers: Record<string, string> = {};
    if (token) headers.Authorization = `Bearer ${token}`;

    void (async () => {
      try {
        el.innerHTML = "";
        const book = ePub(streamUrl, { openAs: "epub", requestHeaders: headers });
        epubBookRef.current = book;
        const rendition = book.renderTo(el, { width: "100%", height: "100%", flow: "paginated" });
        epubRenditionRef.current = rendition;
        rendition.themes.fontSize(`${fontSize}px`);
        rendition.on("relocated", (loc: {
          start?: { cfi?: string; displayed?: { page?: number; total?: number } };
          atStart?: boolean;
          atEnd?: boolean;
        }) => {
          setEpubAtStart(Boolean(loc.atStart));
          setEpubAtEnd(Boolean(loc.atEnd));
          const displayed = loc.start?.displayed;
          if (displayed?.page != null) setEpubPage(displayed.page);
          if (displayed?.total != null) setEpubTotalPages(displayed.total);
          const cfi = loc?.start?.cfi ?? "";
          const page = displayed?.page;
          const total = displayed?.total;
          if (cfi) void persistProgress(cfi, page && total ? page / total : undefined);
        });
        await book.ready;
        if (disposed) return;
        const nav = await book.loaded.navigation;
        if (nav?.toc?.length) {
          setTocItems(nav.toc.map((t) => ({ label: t.label, href: t.href })));
        }
        const resume = epubResumeRef.current;
        if (resume) {
          await rendition.display(resume);
        } else {
          await rendition.display();
        }
        if (!disposed) setEpubReady(true);
      } catch (e) {
        if (!disposed) {
          setEpubReady(false);
          setError(e instanceof Error ? e.message : t("pages.document_reader.epub_open_failed"));
        }
      }
    })();

    return () => {
      disposed = true;
      setEpubReady(false);
      epubBookRef.current?.destroy();
      epubBookRef.current = null;
      epubRenditionRef.current = null;
      el.innerHTML = "";
    };
  }, [mediaId, loading, converting, error, detail?.format, streamUrl, token, persistProgress]);

  useEffect(() => {
    epubRenditionRef.current?.themes.fontSize(`${fontSize}px`);
  }, [fontSize]);

  const epubPrev = useCallback(() => {
    void epubRenditionRef.current?.prev();
  }, []);

  const epubNext = useCallback(() => {
    void epubRenditionRef.current?.next();
  }, []);

  useEffect(() => {
    const fmt = (detail?.format || "").toLowerCase();
    if (fmt !== "epub" || loading || converting || error || !epubReady) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        epubPrev();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        epubNext();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [detail?.format, loading, converting, error, epubReady, epubPrev, epubNext]);

  async function loadPDF(resumePosition: string, url?: string) {
    const pdfUrl = url || streamUrl;
    const headers: Record<string, string> = {};
    if (token) headers.Authorization = `Bearer ${token}`;
    const res = await fetch(pdfUrl, { credentials: "include", headers });
    if (!res.ok) {
      const ct = res.headers.get("content-type") || "";
      if (ct.includes("json")) {
        const body = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(body?.error || `HTTP ${res.status}`);
      }
      throw new Error(`HTTP ${res.status}`);
    }
    const buf = await res.arrayBuffer();
    const doc = await pdfjsLib.getDocument({ data: new Uint8Array(buf) }).promise;
    pdfDocRef.current = doc;
    setPdfPages(doc.numPages);
    let page = 1;
    if (resumePosition.startsWith("page:")) {
      page = Math.max(1, parseInt(resumePosition.slice(5), 10) || 1);
    }
    setPdfPage(page);
    setPdfLoaded(true);
    const outline = await doc.getOutline().catch(() => null);
    if (outline?.length) {
      setTocItems(outline.map((o) => ({ label: o.title, href: `page:${JSON.stringify(o.dest)}` })));
    }
  }

  async function renderPDFPage(doc: pdfjsLib.PDFDocumentProxy, pageNum: number, scale = pdfScale) {
    const page = await doc.getPage(pageNum);
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    const viewport = page.getViewport({ scale });
    canvas.width = viewport.width;
    canvas.height = viewport.height;
    await page.render({ canvasContext: ctx, viewport, canvas }).promise;
    void persistProgress(`page:${pageNum}`, pageNum / doc.numPages);
  }

  useEffect(() => {
    if (!pdfLoaded || !pdfDocRef.current || pdfPage <= 0 || loading || converting) return;
    void renderPDFPage(pdfDocRef.current, pdfPage, pdfScale);
  }, [pdfLoaded, pdfPage, loading, converting, pdfScale]);

  async function loadText(fmt: string, resumePosition: string) {
    const res = await fetch(streamUrl, { credentials: "include" });
    const raw = await res.text();
    if (fmt === "md" || fmt === "mdx") {
      setTextContent(marked.parse(raw) as string);
    } else if (fmt === "csv") {
      setTextContent(raw);
    } else if (fmt === "html" || fmt === "htm") {
      setTextContent(raw);
    } else {
      setTextContent(`<pre>${escapeHtml(raw)}</pre>`);
    }
    if (resumePosition.startsWith("scroll:")) {
      const y = parseInt(resumePosition.slice(7), 10);
      requestAnimationFrame(() => window.scrollTo(0, y));
    }
  }

  function escapeHtml(s: string) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }

  function htmlWithFontSize(html: string, size: number) {
    const style = `<style>html,body{font-size:${size}px!important;line-height:1.7;}</style>`;
    if (/<head[\s>]/i.test(html)) {
      return html.replace(/<head(\s[^>]*)?>/i, (m) => `${m}${style}`);
    }
    return `${style}${html}`;
  }

  const goFullscreen = () => {
    document.documentElement.requestFullscreen?.();
  };

  async function onToggleFavorite() {
    if (!mediaId || Number.isNaN(mediaId) || favoriteBusy) return;
    setFavoriteBusy(true);
    try {
      if (favorited) {
        await removeFavorite(mediaId);
        setFavorited(false);
        message.success(t("pages.document_reader.unfavorited"));
      } else {
        await addFavorite(mediaId);
        setFavorited(true);
        message.success(t("pages.document_reader.favorited"));
      }
    } catch {
      message.error(t("pages.document_reader.favorite_failed"));
    } finally {
      setFavoriteBusy(false);
    }
  }

  const renderCSV = (raw: string) => {
    const lines = raw.split(/\r?\n/).filter(Boolean);
    if (lines.length === 0) return null;
    const rows = lines.map((l) => l.split(","));
    const headers = rows[0];
    const body = rows.slice(1);
    return (
      <table className={styles.csvTable}>
        <thead><tr>{headers.map((h, i) => <th key={i}>{h}</th>)}</tr></thead>
        <tbody>{body.map((row, ri) => <tr key={ri}>{row.map((c, ci) => <td key={ci}>{c}</td>)}</tr>)}</tbody>
      </table>
    );
  };

  const fmt = (detail?.format || "").toLowerCase();
  const isEpub = fmt === "epub";
  const showPdfToolbar = viewAsPdf && pdfPages > 0;
  const showEpubToolbar = isEpub && epubReady && !error && !loading && !converting;
  const isHtml = fmt === "html" || fmt === "htm";
  const isCsv = fmt === "csv";

  return (
    <div className={`${styles.reader} ${styles[`theme-${theme}`]}`} style={{ "--reader-font-size": `${fontSize}px` } as React.CSSProperties}>
      <header className={styles.toolbar}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)}>{t("pages.document_reader.back")}</Button>
        <span className={styles.toolbarTitle}>
          {detail?.title || t("pages.document_reader.default_title")}
        </span>
        {showPdfToolbar && (
          <Space>
            <Button size="small" disabled={pdfPage <= 1} onClick={() => setPdfPage((p) => p - 1)}>{t("pages.document_reader.prev_page")}</Button>
            <span className={styles.toolbarPageInfo}>{pdfPage} / {pdfPages}</span>
            <Button size="small" disabled={pdfPage >= pdfPages} onClick={() => setPdfPage((p) => p + 1)}>{t("pages.document_reader.next_page")}</Button>
          </Space>
        )}
        {showEpubToolbar && (
          <Space>
            <Button size="small" disabled={epubAtStart} onClick={epubPrev}>{t("pages.document_reader.prev_page")}</Button>
            <span className={styles.toolbarPageInfo}>
              {epubPage > 0 && epubTotalPages > 0 ? `${epubPage} / ${epubTotalPages}` : "—"}
            </span>
            <Button size="small" disabled={epubAtEnd} onClick={epubNext}>{t("pages.document_reader.next_page")}</Button>
          </Space>
        )}
        <Button type="text" icon={<MenuOutlined />} onClick={() => setTocOpen((v) => !v)} />
        <Button
          type="text"
          icon={<MinusOutlined />}
          disabled={fontSize <= 12}
          aria-label={t("pages.document_reader.zoom_out_aria")}
          onClick={() => setFontSize((s) => Math.max(12, s - 2))}
        />
        <Button
          type="text"
          icon={<PlusOutlined />}
          disabled={fontSize >= 28}
          aria-label={t("pages.document_reader.zoom_in_aria")}
          onClick={() => setFontSize((s) => Math.min(28, s + 2))}
        />
        <Select size="small" value={theme} onChange={setTheme} options={[
          { value: "light", label: t("pages.document_reader.theme_light") },
          { value: "sepia", label: t("pages.document_reader.theme_sepia") },
          { value: "dark", label: t("pages.document_reader.theme_dark") },
        ]} />
        <Input
          size="small"
          placeholder={t("pages.document_reader.search_placeholder")}
          prefix={<SearchOutlined />}
          value={searchQ}
          onChange={(e) => setSearchQ(e.target.value)}
          onPressEnter={() => {
            if (searchQ && fmt !== "pdf") {
              const w = window as Window & { find?: (s: string) => boolean };
              w.find?.(searchQ);
            } else {
              message.info(t("pages.document_reader.pdf_search_hint"));
            }
          }}
          style={{ width: 120 }}
        />
        <Button
          type="text"
          icon={favorited ? <StarFilled /> : <StarOutlined />}
          aria-label={
            favorited
              ? t("pages.document_reader.aria_unfavorite")
              : t("pages.document_reader.aria_favorite")
          }
          loading={favoriteBusy}
          onClick={() => void onToggleFavorite()}
          className={favorited ? styles.toolbarFavoriteActive : undefined}
        />
        <Button type="text" icon={<FullscreenOutlined />} onClick={goFullscreen} />
      </header>

      <div className={styles.body}>
        {tocOpen && tocItems.length > 0 && (
          <aside className={styles.tocPanel}>
            {tocItems.map((toc, i) => (
              <div
                key={i}
                className={styles.tocItem}
                onClick={() => {
                  if (isEpub && epubRenditionRef.current) {
                    void epubRenditionRef.current.display(toc.href);
                  } else if (toc.href.startsWith("page:")) {
                    setPdfPage(parseInt(toc.href.slice(5), 10) || 1);
                  }
                }}
              >
                {toc.label}
              </div>
            ))}
          </aside>
        )}

        <div className={`${styles.content}${isEpub && !error && !loading && !converting ? ` ${styles.contentEpub}` : ""}`}>
          {(loading || converting) && (
            <div style={{ textAlign: "center", padding: 48 }}>
              <Spin size="large" />
              {converting && <div style={{ marginTop: 12, opacity: 0.7 }}>{t("pages.document_reader.converting_office")}</div>}
            </div>
          )}
          {error && (
            <div className={styles.errorBox}>
              <p>{error}</p>
              <Button type="primary" href={documentDownloadSrc(mediaId, token)}>{t("pages.document_reader.download_original")}</Button>
            </div>
          )}
          {viewAsPdf && !error && !loading && !converting && (
            <div className={styles.pdfCanvasWrap}>
              <canvas ref={canvasRef} />
            </div>
          )}
          {isEpub && !error && !loading && !converting && (
            <div className={styles.epubWrap}>
              <div ref={epubRef} className={styles.epubView} />
              <button
                type="button"
                className={`${styles.epubNavZone} ${styles.epubNavZonePrev}`}
                disabled={epubAtStart}
                aria-label={t("pages.document_reader.prev_page_aria")}
                onClick={epubPrev}
              />
              <button
                type="button"
                className={`${styles.epubNavZone} ${styles.epubNavZoneNext}`}
                disabled={epubAtEnd}
                aria-label={t("pages.document_reader.next_page_aria")}
                onClick={epubNext}
              />
            </div>
          )}
          {isHtml && !error && !loading && !converting && (
            <iframe
              className={styles.htmlFrame}
              sandbox="allow-same-origin"
              srcDoc={htmlWithFontSize(textContent, fontSize)}
              title="HTML preview"
            />
          )}
          {isCsv && !error && !loading && !converting && (
            <div className={styles.textBody}>{renderCSV(textContent)}</div>
          )}
          {!isHtml && !isCsv && !viewAsPdf && fmt !== "epub" && !error && !loading && !converting && (
            <div className={`${styles.textBody} markdown`} dangerouslySetInnerHTML={{ __html: textContent }} />
          )}
        </div>
      </div>
    </div>
  );
}
