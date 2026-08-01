import {
  Alert,
  Avatar,
  Button,
  Card,
  Modal,
  Col,
  Collapse,
  Descriptions,
  Divider,
  Form,
  Image,
  Input,
  InputNumber,
  List,
  Row,
  Select,
  Space,
  Tree,
  Typography,
  Tag,
  message,
} from "antd";
import type { DataNode } from "antd/es/tree";
import { useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { EditOutlined } from "@ant-design/icons";
import {
  addMediaPerson,
  deleteMedia,
  deleteMediaPersonLink,
  fetchLibraries,
  fetchAdminMedia,
  fetchMediaDetail,
  fetchMediaDeletionPlan,
  fetchMediaPersons,
  retryAdminMediaIngest,
  type AdminMediaItem,
  type Library,
  type MediaDetail,
  type MediaPersonLink,
  updateMediaAdmin,
} from "../api/client";
import { useT } from "../i18n";
import MediaImagePickerDialog, { autoFrameForMedia } from "../components/MediaImagePickerDialog";

type EditorValues = {
  title?: string;
  original_title?: string;
  status?: string;
  duration?: number;
  width?: number;
  height?: number;
  bitrate?: number;
  format?: string;
  overview?: string;
  rating?: number;
  year?: number;
  genres?: string[];
  directors?: string[];
  countries?: string[];
  writers?: string[];
  producers?: string[];
  actors?: string[];
  poster?: string;
  backdrop?: string;
  logo?: string;
  meta_json?: string;
};

function uniqueStrings(items: string[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of items) {
    const s = (raw || "").trim();
    if (!s) continue;
    const key = s.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(s);
  }
  return out;
}

function readStringList(...sources: unknown[]): string[] {
  const out: string[] = [];
  for (const src of sources) {
    if (typeof src === "string") {
      for (const part of src.split(/[,銆?|]/)) {
        out.push(part.trim());
      }
      continue;
    }
    if (!Array.isArray(src)) continue;
    for (const item of src) {
      if (typeof item === "string") {
        out.push(item.trim());
      } else if (item && typeof item === "object") {
        const name = (item as { name?: unknown }).name;
        if (typeof name === "string") out.push(name.trim());
      }
    }
  }
  return uniqueStrings(out);
}

function readCastActorNames(extra: Record<string, unknown>): string[] {
  const cast = extra.cast ?? extra.actors;
  if (!Array.isArray(cast)) return [];
  return uniqueStrings(
    cast.map((item) => {
      if (typeof item === "string") return item;
      if (item && typeof item === "object") {
        const row = item as { name?: unknown; actor?: unknown };
        if (typeof row.name === "string") return row.name;
        if (typeof row.actor === "string") return row.actor;
      }
      return "";
    }),
  );
}

function readCountryList(extra: Record<string, unknown>): string[] {
  return readStringList(extra.country, extra.countries, extra.production_countries, extra.origin_country);
}

function personNamesByOccupation(links: MediaPersonLink[], occupation: string): string[] {
  return uniqueStrings(links.filter((l) => l.occupation === occupation).map((l) => l.person_name));
}

function crewNamesFromExtra(extra: Record<string, unknown>, scrape: Record<string, unknown>) {
  return {
    directors: readStringList(extra.director, extra.directors, extra.crew),
    writers: readStringList(extra.writer, extra.writers, extra.author, extra.authors),
    producers: readStringList(extra.producer, extra.producers),
    actors: uniqueStrings([...readCastActorNames(extra), ...readStringList(extra.actors)]),
    genres: uniqueStrings(readStringList(scrape.genres, extra.genres)),
  };
}

function hasAnyCrewMetaKeys(extra: Record<string, unknown>): boolean {
  return [
    "director",
    "directors",
    "crew",
    "writer",
    "writers",
    "author",
    "authors",
    "producer",
    "producers",
    "cast",
    "actors",
  ].some((key) => extra[key] != null);
}

function crewNamesForForm(
  extra: Record<string, unknown>,
  scrape: Record<string, unknown>,
  links: MediaPersonLink[],
) {
  const fromMeta = crewNamesFromExtra(extra, scrape);
  if (hasAnyCrewMetaKeys(extra)) return fromMeta;
  return {
    directors: personNamesByOccupation(links, "director"),
    writers: personNamesByOccupation(links, "writer"),
    producers: personNamesByOccupation(links, "producer"),
    actors: personNamesByOccupation(links, "actor"),
    genres: fromMeta.genres,
  };
}

function normalizePersonName(name: string): string {
  return (name || "").trim().toLowerCase();
}

async function syncMediaPersonCrew(
  mediaId: number,
  crew: { directors: string[]; writers: string[]; producers: string[]; actors: string[] },
) {
  const { items: links } = await fetchMediaPersons(mediaId);
  const groups: Array<{ occupation: string; names: string[] }> = [
    { occupation: "director", names: crew.directors },
    { occupation: "writer", names: crew.writers },
    { occupation: "producer", names: crew.producers },
    { occupation: "actor", names: crew.actors },
  ];
  for (const group of groups) {
    const keep = new Set(group.names.map(normalizePersonName).filter(Boolean));
    const occLinks = links.filter((l) => l.occupation === group.occupation);
    for (const link of occLinks) {
      if (!keep.has(normalizePersonName(link.person_name))) {
        await deleteMediaPersonLink(mediaId, link.id);
      }
    }
    const linked = new Set(
      occLinks
        .filter((l) => keep.has(normalizePersonName(l.person_name)))
        .map((l) => normalizePersonName(l.person_name)),
    );
    for (const name of group.names) {
      const key = normalizePersonName(name);
      if (!key || linked.has(key)) continue;
      await addMediaPerson(mediaId, { name: name.trim(), occupation: group.occupation });
      linked.add(key);
    }
  }
}

function tagSelectProps(placeholder: string) {
  return {
    mode: "tags" as const,
    allowClear: true,
    placeholder,
    tokenSeparators: [","],
    style: { width: "100%" },
  };
}

function readScrapeYear(
  detailYear?: number,
  scrape?: Record<string, unknown>,
  parsed?: Record<string, unknown>,
): number | undefined {
  if (detailYear != null && detailYear > 0) return detailYear;
  const sy = scrape?.year;
  if (typeof sy === "number" && sy >= 1800 && sy <= 2100) return sy;
  if (typeof sy === "string" && sy.trim()) {
    const n = Number(sy.trim());
    if (n >= 1800 && n <= 2100) return n;
  }
  const py = parsed?.year;
  if (typeof py === "number" && py >= 1800 && py <= 2100) return py;
  const rd = scrape?.release_date;
  if (typeof rd === "string" && rd.trim().length >= 4) {
    const n = Number(rd.trim().slice(0, 4));
    if (n >= 1800 && n <= 2100) return n;
  }
  return undefined;
}

function applyYearToScrape(scrape: Record<string, any>, parsed: Record<string, any>, year?: number) {
  if (typeof year !== "number" || year < 1800 || year > 2100) {
    delete scrape.year;
    delete parsed.year;
    return;
  }
  scrape.year = year;
  parsed.year = year;
  const existing = typeof scrape.release_date === "string" ? scrape.release_date.trim() : "";
  if (/^\d{4}-\d{2}-\d{2}$/.test(existing) || /^\d{4}-\d{2}$/.test(existing)) {
    scrape.release_date = `${year}${existing.slice(4)}`;
  } else {
    scrape.release_date = String(year);
  }
}

type TreeNodeInfo = {
  type: "dir" | "file";
  key: string;
  name: string;
  path: string;
  mediaId?: number;
};

function safeParseMeta(raw?: string): Record<string, any> {
  const text = (raw || "").trim();
  if (!text) return {};
  try {
    const parsed = JSON.parse(text) as Record<string, any>;
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

function stringifyMeta(meta: Record<string, any>): string {
  return JSON.stringify(meta, null, 2);
}

function normalizePath(raw: string) {
  return (raw || "").replace(/\\/g, "/");
}

function toLibraryRelativePath(fullPath: string, libraryRoots?: string[]) {
  let full = normalizePath(fullPath).replace(/\/+$/, "");
  if (full.toLowerCase().startsWith("//?/unc/")) {
    full = "//" + full.slice("//?/unc/".length);
  } else if (full.toLowerCase().startsWith("//?/")) {
    full = full.slice(4);
  }
  const roots = (libraryRoots || [])
    .map((r) => normalizePath(r || "").replace(/\/+$/, ""))
    .filter(Boolean)
    .sort((a, b) => b.length - a.length);
  if (roots.length === 0) return full;
  const fullLower = full.toLowerCase();
  for (const root of roots) {
    const rootLower = root.toLowerCase();
    if (fullLower === rootLower) return "";
    if (fullLower.startsWith(`${rootLower}/`)) {
      return full.slice(root.length + 1);
    }
  }
  return full;
}

/** Strip Windows drive segments left over when root matching fails, so the tree lists folders under the library instead of k:/ f:/ roots. */
function stripLeadingWindowsDriveSegments(rel: string): string {
  const parts = normalizePath(rel)
    .replace(/^\/+/, "")
    .split("/")
    .filter(Boolean);
  while (parts.length > 0 && /^[a-zA-Z]:$/.test(parts[0])) {
    parts.shift();
  }
  return parts.join("/");
}

/** Relative path for tree, lists, and directory selection (never shows a leading drive letter as a fake root). */
function toLibraryDisplayRelativePath(fullPath: string, libraryRoots?: string[]) {
  return stripLeadingWindowsDriveSegments(toLibraryRelativePath(fullPath, libraryRoots));
}

function nodeTitle(name: string, kind: "dir" | "file") {
  return <span>{kind === "dir" ? `馃搧 ${name}` : `馃幀 ${name}`}</span>;
}

function isRequestCancellation(error: unknown, signal: AbortSignal) {
  if (signal.aborted) return true;
  if (error instanceof DOMException && error.name === "AbortError") return true;
  if (!error || typeof error !== "object") return false;
  const candidate = error as { name?: string; code?: string };
  return candidate.name === "CanceledError" || candidate.code === "ERR_CANCELED";
}

export default function MediaManagerPage() {
  const t = useT();
  const [searchParams] = useSearchParams();
  // ?media_id=<id> (or ?id=<id>) requests auto-selection of a specific media item,
  // e.g. when the user clicks "Edit" on a media detail page. The ref holds the
  // pending target only for the initial mount so subsequent in-app navigation
  // behaves normally.
  const initialTargetId = (() => {
    const raw = searchParams.get("media_id") || searchParams.get("id");
    if (!raw) return null;
    const n = Number(raw);
    return Number.isFinite(n) && n > 0 ? n : null;
  })();
  const pendingTargetIdRef = useRef<number | null>(initialTargetId);
  const [libs, setLibs] = useState<Library[]>([]);
  const [libraryId, setLibraryId] = useState<number | undefined>(undefined);
  const [rows, setRows] = useState<AdminMediaItem[]>([]);
  const [mediaLoading, setMediaLoading] = useState(false);
  const [mediaLoadMoreLoading, setMediaLoadMoreLoading] = useState(false);
  const [mediaLoadMoreError, setMediaLoadMoreError] = useState(false);
  const [mediaNextCursor, setMediaNextCursor] = useState<string | undefined>();
  const [mediaHasMore, setMediaHasMore] = useState(false);
  const mediaRequestSequenceRef = useRef(0);
  const mediaControllerRef = useRef<AbortController | null>(null);
  const detailRequestSequenceRef = useRef(0);
  const detailControllerRef = useRef<AbortController | null>(null);
  const [selectedNode, setSelectedNode] = useState<TreeNodeInfo | null>(null);
  const [loadingDetail, setLoadingDetail] = useState(false);
  const [saving, setSaving] = useState(false);
  const [detail, setDetail] = useState<MediaDetail | null>(null);
  const [treeKeyword, setTreeKeyword] = useState("");
  const [posterPickerOpen, setPosterPickerOpen] = useState(false);
  const [backdropPickerOpen, setBackdropPickerOpen] = useState(false);
  const [logoPickerOpen, setLogoPickerOpen] = useState(false);
  const [genreOptions, setGenreOptions] = useState<string[]>([]);
  const [form] = Form.useForm<EditorValues>();
  const posterPreview = Form.useWatch("poster", form);
  const backdropPreview = Form.useWatch("backdrop", form);
  const logoPreview = Form.useWatch("logo", form);
  const watchedGenres = Form.useWatch("genres", form);
  const selectedId = selectedNode?.type === "file" ? selectedNode.mediaId : undefined;
  const selectedLibrary = useMemo(
    () => libs.find((l) => l.id === libraryId),
    [libs, libraryId]
  );
  const selectedLibraryRoots = useMemo(() => {
    const roots = [...(selectedLibrary?.folders || []), selectedLibrary?.path || ""]
      .map((x) => (x || "").trim())
      .filter(Boolean);
    return Array.from(new Set(roots));
  }, [selectedLibrary?.folders, selectedLibrary?.path]);

  async function loadLibraries() {
    const items = await fetchLibraries();
    setLibs(items);
    if (items.length === 0) return;
    const target = pendingTargetIdRef.current;
    if (target != null) {
      // Switch to the library that actually owns the target media so the tree
      // highlights the right file instead of defaulting to the first library.
      try {
        const d = await fetchMediaDetail(target);
        const ownerLib = items.find((l) => l.id === d.library_id);
        setLibraryId(ownerLib ? ownerLib.id : items[0].id);
      } catch {
        setLibraryId(items[0].id);
      }
      return;
    }
    setLibraryId((current) => (current !== undefined ? current : items[0].id));
  }

  function applyLoadedMedia(items: AdminMediaItem[]) {
    const target = pendingTargetIdRef.current;
    if (target != null) {
      const hit = items.find((x) => x.id === target);
      pendingTargetIdRef.current = null;
      if (hit) {
        setSelectedNode({
          type: "file",
          key: `file:${hit.id}`,
          name: hit.title || hit.file_id,
          path: toLibraryDisplayRelativePath(hit.file_path || "", selectedLibraryRoots),
          mediaId: hit.id,
        });
        return;
      }
    }
    setSelectedNode((current) => {
      if (current?.type === "file" && items.some((x) => x.id === current.mediaId)) return current;
      const first = items[0];
      return first ? {
        type: "file",
        key: `file:${first.id}`,
        name: first.title || first.file_id,
        path: toLibraryDisplayRelativePath(first.file_path || "", selectedLibraryRoots),
        mediaId: first.id,
      } : null;
    });
  }

  async function loadMediaPage(libId: number, cursor: string | undefined, append: boolean, preserveDuringLoad = false) {
    const sequence = append ? mediaRequestSequenceRef.current : ++mediaRequestSequenceRef.current;
    mediaControllerRef.current?.abort();
    const controller = new AbortController();
    mediaControllerRef.current = controller;
    if (append) {
      setMediaLoadMoreLoading(true);
      setMediaLoadMoreError(false);
    } else if (!preserveDuringLoad) {
      detailControllerRef.current?.abort();
      detailRequestSequenceRef.current++;
      setRows([]);
      setSelectedNode(null);
      setDetail(null);
      setGenreOptions([]);
      form.resetFields();
      setMediaNextCursor(undefined);
      setMediaHasMore(false);
      setMediaLoadMoreError(false);
      setMediaLoading(true);
    }
    try {
      const page = await fetchAdminMedia(
        { library_id: libId, sort: "id_desc", limit: 500, ...(cursor ? { cursor } : {}) },
        controller.signal,
      );
      if (controller.signal.aborted || sequence !== mediaRequestSequenceRef.current || libId !== libraryId) return;
      let merged: AdminMediaItem[] = [];
      setRows((previous) => {
        const base = append ? previous : [];
        const seen = new Set(base.map((item) => item.id));
        merged = [...base];
        for (const item of page.items) {
          if (seen.has(item.id)) continue;
          const current = previous.find((existing) => existing.id === item.id);
          const keepNewerProcessing = current?.ingest_run_status === "processing"
            && (current.ingest_generation ?? 0) > (item.ingest_generation ?? 0);
          seen.add(item.id);
          merged.push(keepNewerProcessing ? current : item);
        }
        return merged;
      });
      const nextCursor = page.next_cursor?.trim();
      const canContinue = page.has_more && Boolean(nextCursor) && nextCursor !== cursor;
      setMediaNextCursor(canContinue ? nextCursor : undefined);
      setMediaHasMore(canContinue);
      applyLoadedMedia(merged);
    } catch (e) {
      if (controller.signal.aborted || sequence !== mediaRequestSequenceRef.current) return;
      if (append) setMediaLoadMoreError(true);
      else message.error((e as Error).message || t("pages.media_manager.load_media_failed"));
    } finally {
      if (sequence === mediaRequestSequenceRef.current && mediaControllerRef.current === controller) {
        if (append) setMediaLoadMoreLoading(false);
        else setMediaLoading(false);
      }
    }
  }

  async function loadDetail(id: number) {
    const sequence = ++detailRequestSequenceRef.current;
    detailControllerRef.current?.abort();
    const controller = new AbortController();
    detailControllerRef.current = controller;
    setLoadingDetail(true);
    try {
      const [d, personLinks] = await Promise.all([
        fetchMediaDetail(id, controller.signal),
        fetchMediaPersons(id, controller.signal).catch(() => ({ items: [] as MediaPersonLink[], resolved: [] })),
      ]);
      if (controller.signal.aborted || sequence !== detailRequestSequenceRef.current) return;
      setDetail(d);
      form.setFieldsValue({
        title: d.title || "",
        original_title: d.original_title || "",
        status: d.status || "active",
        duration: d.duration || 0,
        width: d.width || 0,
        height: d.height || 0,
        bitrate: d.bitrate || 0,
        format: d.format || "",
        meta_json: stringifyMeta(safeParseMeta(d.meta_json)),
      });
      const parsed = safeParseMeta(d.meta_json);
      const scrape = (parsed.scrape || {}) as Record<string, any>;
      const extra = (scrape.extra || {}) as Record<string, any>;
      const crew = crewNamesForForm(extra, scrape, personLinks.items);
      setGenreOptions(crew.genres);
      form.setFieldsValue({
        overview: typeof scrape.overview === "string" ? scrape.overview : "",
        rating: typeof scrape.rating === "number" ? scrape.rating : undefined,
        year: readScrapeYear(d.year, scrape, parsed),
        genres: crew.genres,
        directors: crew.directors,
        countries: readCountryList(extra),
        writers: crew.writers,
        producers: crew.producers,
        actors: crew.actors,
        poster: typeof extra.poster === "string" ? extra.poster : "",
        backdrop: typeof extra.backdrop === "string" ? extra.backdrop : "",
        logo: typeof extra.logo === "string" ? extra.logo : "",
      });
    } catch (e) {
      if (
        isRequestCancellation(e, controller.signal) ||
        sequence !== detailRequestSequenceRef.current ||
        detailControllerRef.current !== controller
      ) return;
      throw e;
    } finally {
      if (sequence === detailRequestSequenceRef.current && detailControllerRef.current === controller) setLoadingDetail(false);
    }
  }

  useEffect(() => {
    void loadLibraries().catch((e: unknown) => message.error((e as Error).message || t("pages.media_manager.load_libraries_failed")));
     
  }, []);

  useEffect(() => {
    if (libraryId === undefined) return;
    void loadMediaPage(libraryId, undefined, false);
    return () => {
      mediaControllerRef.current?.abort();
      detailControllerRef.current?.abort();
      mediaRequestSequenceRef.current++;
      detailRequestSequenceRef.current++;
    };
  }, [libraryId]);

  useEffect(() => {
    if (!selectedId) return;
    const sequence = detailRequestSequenceRef.current + 1;
    void loadDetail(selectedId).catch((e: unknown) => {
      if (sequence === detailRequestSequenceRef.current && !detailControllerRef.current?.signal.aborted) {
        message.error((e as Error).message || t("pages.media_manager.load_detail_failed"));
      }
    });
    return () => detailControllerRef.current?.abort();
  }, [selectedId]);

  const { treeData, treeMap } = useMemo(() => {
    const root: DataNode[] = [];
    const map = new Map<string, TreeNodeInfo>();
    const getOrCreateDir = (segments: string[], fullPath: string): DataNode => {
      let cursor = root;
      let node: DataNode | undefined;
      let acc = "";
      segments.forEach((seg) => {
        acc = acc ? `${acc}/${seg}` : seg;
        let found = cursor.find((n) => n.key === `dir:${acc}`);
        if (!found) {
          found = {
            key: `dir:${acc}`,
            title: nodeTitle(seg, "dir"),
            children: [],
          };
          cursor.push(found);
          map.set(`dir:${acc}`, { type: "dir", key: `dir:${acc}`, name: seg, path: fullPath });
        }
        node = found;
        cursor = (found.children || []) as DataNode[];
      });
      return node!;
    };
    rows.forEach((m) => {
      const rel = toLibraryDisplayRelativePath(m.file_path || "", selectedLibraryRoots);
      const parts = rel.split("/").filter(Boolean);
      const fileName = parts.length > 0 ? parts[parts.length - 1] : String(m.id);
      const dirs = parts.slice(0, -1);
      let parentChildren = root;
      if (dirs.length > 0) {
        const dirNode = getOrCreateDir(dirs, dirs.join("/"));
        parentChildren = (dirNode.children || []) as DataNode[];
      }
      const fileKey = `file:${m.id}`;
      parentChildren.push({
        key: fileKey,
        title: nodeTitle(m.title || fileName, "file"),
        isLeaf: true,
      });
      map.set(fileKey, {
        type: "file",
        key: fileKey,
        mediaId: m.id,
        name: m.title || fileName,
        path: rel,
      });
    });
    return { treeData: root, treeMap: map };
  }, [rows, selectedLibraryRoots]);

  const filteredTreeData = useMemo(() => {
    const kw = treeKeyword.trim().toLowerCase();
    if (!kw) return treeData;
    const pass = (node: DataNode): DataNode | null => {
      const info = treeMap.get(String(node.key));
      const hit =
        !!info &&
        (info.name.toLowerCase().includes(kw) ||
          info.path.toLowerCase().includes(kw));
      const children = (node.children || [])
        .map((c) => pass(c as DataNode))
        .filter((c): c is DataNode => !!c);
      if (hit || children.length > 0) {
        return { ...node, children };
      }
      return null;
    };
    return treeData
      .map((n) => pass(n))
      .filter((n): n is DataNode => !!n);
  }, [treeData, treeMap, treeKeyword]);

  const dirFiles = useMemo(() => {
    if (selectedNode?.type !== "dir") return [];
    const prefix = selectedNode.path ? `${selectedNode.path}/` : "";
    return rows
      .filter((x) => {
        const p = toLibraryDisplayRelativePath(x.file_path || "", selectedLibraryRoots);
        return p.startsWith(prefix) && p !== selectedNode.path;
      })
      .sort((a, b) =>
        toLibraryDisplayRelativePath(a.file_path || "", selectedLibraryRoots).localeCompare(
          toLibraryDisplayRelativePath(b.file_path || "", selectedLibraryRoots)
        )
      );
  }, [rows, selectedNode, selectedLibraryRoots]);

  const onSave = async () => {
    if (!selectedId) return;
    const v = await form.validateFields();
    const parsed = safeParseMeta(v.meta_json);
    const scrape = (parsed.scrape && typeof parsed.scrape === "object" ? parsed.scrape : {}) as Record<string, any>;
    const extra = (scrape.extra && typeof scrape.extra === "object" ? scrape.extra : {}) as Record<string, any>;
    scrape.overview = (v.overview || "").trim();
    if (typeof v.rating === "number") {
      scrape.rating = v.rating;
    } else {
      delete scrape.rating;
    }
    applyYearToScrape(scrape, parsed, v.year);
    const genres = uniqueStrings(v.genres || []);
    scrape.genres = genres;
    if (genres.length > 0) {
      extra.genres = genres;
    } else {
      delete extra.genres;
    }
    setGenreOptions((prev) => uniqueStrings([...prev, ...genres]));

    const directors = uniqueStrings(v.directors || []);
    if (directors.length > 0) {
      extra.directors = directors;
      extra.director = directors;
    } else {
      delete extra.directors;
      delete extra.director;
    }

    const countries = uniqueStrings(v.countries || []);
    if (countries.length > 0) {
      extra.countries = countries;
      extra.country = countries.length === 1 ? countries[0] : countries.join(" / ");
    } else {
      delete extra.countries;
      delete extra.country;
    }

    const writers = uniqueStrings(v.writers || []);
    if (writers.length > 0) {
      extra.writers = writers;
      extra.writer = writers;
    } else {
      delete extra.writers;
      delete extra.writer;
    }

    const producers = uniqueStrings(v.producers || []);
    if (producers.length > 0) {
      extra.producers = producers;
      extra.producer = producers;
    } else {
      delete extra.producers;
      delete extra.producer;
    }

    const actors = uniqueStrings(v.actors || []);
    if (actors.length > 0) {
      extra.cast = actors.map((name) => ({ name }));
      extra.actors = actors;
    } else {
      delete extra.cast;
      delete extra.actors;
    }

    extra.poster = (v.poster || "").trim();
    extra.backdrop = (v.backdrop || "").trim();
    extra.logo = (v.logo || "").trim();
    scrape.extra = extra;
    parsed.scrape = scrape;
    const mergedMetaJSON = stringifyMeta(parsed);
    const crewPayload = {
      directors,
      writers,
      producers,
      actors,
    };
    setSaving(true);
    try {
      await updateMediaAdmin(selectedId, {
        title: v.title ?? "",
        original_title: v.original_title ?? "",
        status: v.status ?? "active",
        duration: Number(v.duration ?? 0),
        width: Number(v.width ?? 0),
        height: Number(v.height ?? 0),
        bitrate: Number(v.bitrate ?? 0),
        format: v.format ?? "",
        meta_json: mergedMetaJSON,
      });
      await syncMediaPersonCrew(selectedId, crewPayload);
      message.success(t("pages.media_manager.saved"));
      if (libraryId !== undefined) await loadMediaPage(libraryId, undefined, false);
      await loadDetail(selectedId);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.media_manager.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const effectivePublicationState = (item: AdminMediaItem): AdminMediaItem["publication_state"] =>
    item.ingest_run_status === "processing" ? "processing" : item.publication_state;

  const actionablePublication = (state: AdminMediaItem["publication_state"]) =>
    state === "failed" || state === "degraded";

  async function onRetryIngest(mediaId: number) {
    try {
      const response = await retryAdminMediaIngest(mediaId);
      setRows((previous) => previous.map((item) => item.id === mediaId ? {
        ...item,
        publication_state: response.media.publication_state,
        publication_error: response.media.publication_error,
        ingest_generation: response.media.ingest_generation,
        ingest_run_status: response.run.status,
      } : item));
      message.success(t("pages.media_manager.ingest_retry_success"));
      if (libraryId !== undefined) await loadMediaPage(libraryId, undefined, false, true);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.media_manager.ingest_retry_failed"));
    }
  }

  function onRemoveIngest(item: AdminMediaItem) {
    void (async () => {
      let files: string[] = [];
      try {
        files = await fetchMediaDeletionPlan(item.id);
      } catch {
        files = [item.file_path].filter(Boolean) as string[];
      }
      if (files.length === 0 && item.file_path) {
        files = [item.file_path];
      }
      Modal.confirm({
        title: t("pages.media_manager.ingest_remove_confirm_title"),
        okText: t("pages.media_manager.ingest_remove_confirm_ok"),
        okButtonProps: { danger: true },
        content: (
          <div>
            <p style={{ marginBottom: 8 }}>{t("pages.media_manager.ingest_remove_warning")}</p>
            {files.length > 0 ? (
              <ul style={{ margin: "0 0 12px", paddingLeft: 20, wordBreak: "break-all" }}>
                {files.map((f) => <li key={f}>{f}</li>)}
              </ul>
            ) : null}
          </div>
        ),
        onOk: async () => {
          try {
            await deleteMedia(item.id);
            message.success(t("pages.media_manager.ingest_remove_success"));
            setRows((prev) => prev.filter((r) => r.id !== item.id));
            if (selectedId === item.id) {
              detailControllerRef.current?.abort();
              detailRequestSequenceRef.current++;
              setSelectedNode(null);
              setDetail(null);
              setGenreOptions([]);
              form.resetFields();
            }
          } catch (e: unknown) {
            message.error((e as Error).message || t("pages.media_manager.ingest_remove_failed"));
            throw e;
          }
        },
      });
    })();
  }

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("pages.media_manager.intro")}
      </Typography.Paragraph>

      <Card title={t("pages.media_manager.ingest_status_title")} loading={mediaLoading}>
        <div style={{ maxHeight: 320, overflowY: "auto" }}>
          <List
            size="small"
            dataSource={rows.filter((item) => effectivePublicationState(item) !== "published")}
            locale={{ emptyText: t("pages.media_manager.ingest_status_empty") }}
            renderItem={(item) => {
              const effectiveState = effectivePublicationState(item);
              const showActions = effectiveState !== "processing" && actionablePublication(item.publication_state);
              const reason = (item.publication_error || "").trim();
              return (
                <List.Item
                  actions={
                    showActions
                      ? [
                          <Button key="retry" type="link" size="small" aria-label={t("pages.media_manager.ingest_retry")} onClick={() => void onRetryIngest(item.id)}>
                            {t("pages.media_manager.ingest_retry")}
                          </Button>,
                          <Button key="remove" type="link" size="small" danger aria-label={t("pages.media_manager.ingest_remove")} onClick={() => onRemoveIngest(item)}>
                            {t("pages.media_manager.ingest_remove")}
                          </Button>,
                        ]
                      : undefined
                  }
                >
                  <List.Item.Meta
                    title={item.title || item.file_id}
                    description={
                      <Space direction="vertical" size={0} style={{ width: "100%" }}>
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>{item.file_path}</Typography.Text>
                        {showActions ? (
                          <Typography.Text type="danger" style={{ fontSize: 12 }}>
                            {reason || t("pages.media_manager.ingest_no_reason")}
                          </Typography.Text>
                        ) : null}
                      </Space>
                    }
                  />
                  <Tag
                    color={effectiveState === "processing" ? "processing" : effectiveState === "degraded" ? "warning" : "error"}
                    role="status"
                    aria-label={t(`pages.media_manager.publication_${effectiveState}`)}
                  >
                    {t(`pages.media_manager.publication_${effectiveState}`)}
                  </Tag>
                </List.Item>
              );
            }}
          />
        </div>
        {mediaHasMore || mediaLoadMoreError ? (
          <Button
            block
            style={{ marginTop: 8 }}
            loading={mediaLoadMoreLoading}
            aria-label={mediaLoadMoreLoading
              ? t("pages.media_manager.ingest_load_more_loading")
              : mediaLoadMoreError
                ? t("pages.media_manager.ingest_load_more_retry")
                : t("pages.media_manager.ingest_load_more")}
            onClick={() => libraryId !== undefined && void loadMediaPage(libraryId, mediaNextCursor, true)}
          >
            {mediaLoadMoreError ? t("pages.media_manager.ingest_load_more_retry") : t("pages.media_manager.ingest_load_more")}
          </Button>
        ) : null}
      </Card>

      <Row gutter={16}>
        <Col xs={24} lg={11}>
          <Card
            title={t("pages.media_manager.tree_title")}
            extra={
              <Select
                style={{ width: 220 }}
                placeholder={t("pages.media_manager.library_placeholder")}
                value={libraryId}
                onChange={(v) => setLibraryId(v)}
                options={libs.map((l) => ({ value: l.id, label: l.name }))}
              />
            }
          >
            <Input
              allowClear
              placeholder={t("pages.media_manager.filter_placeholder")}
              value={treeKeyword}
              onChange={(e) => setTreeKeyword(e.target.value)}
              style={{ marginBottom: 10 }}
            />
            <Tree
              treeData={filteredTreeData}
              height={620}
              defaultExpandAll
              selectedKeys={selectedNode ? [selectedNode.key] : []}
              onSelect={(keys) => {
                const key = String(keys[0] || "");
                const node = treeMap.get(key);
                if (node) {
                  setSelectedNode(node);
                  if (node.type === "dir") {
                    setDetail(null);
                    form.resetFields();
                  }
                }
              }}
            />
          </Card>
        </Col>

        <Col xs={24} lg={13}>
          {selectedNode?.type === "dir" ? (
            <Card title={t("pages.media_manager.dir_info_prefix", { name: selectedNode.name })}>
              <Descriptions column={1} bordered size="small">
                <Descriptions.Item label={t("pages.media_manager.dir_name_label")}>{selectedNode.name}</Descriptions.Item>
                <Descriptions.Item label={t("pages.media_manager.dir_path_label")}>{selectedNode.path}</Descriptions.Item>
                <Descriptions.Item label={t("pages.media_manager.dir_file_count_label")}>
                  {rows.filter((x) => toLibraryDisplayRelativePath(x.file_path || "", selectedLibraryRoots).startsWith(selectedNode.path)).length}
                </Descriptions.Item>
              </Descriptions>
              <Collapse
                size="small"
                style={{ marginTop: 12 }}
                items={[
                  {
                    key: "debug-root",
                    label: t("pages.media_manager.debug_panel_title"),
                    children: (
                      <Descriptions column={1} bordered size="small">
                        <Descriptions.Item label={t("pages.media_manager.lib_root_path_label")}>
                          {selectedLibrary?.path || "-"}
                        </Descriptions.Item>
                      </Descriptions>
                    ),
                  },
                ]}
              />
              <Divider />
              <Space style={{ marginBottom: 8 }}>
                <Typography.Text strong>{t("pages.media_manager.dir_files_label")}</Typography.Text>
                <Button
                  size="small"
                  disabled={dirFiles.length === 0}
                  onClick={() => {
                    const first = dirFiles[0];
                    if (!first) return;
                    setSelectedNode({
                      type: "file",
                      key: `file:${first.id}`,
                      name: first.title || first.file_id,
                            path: toLibraryDisplayRelativePath(first.file_path || "", selectedLibraryRoots),
                      mediaId: first.id,
                    });
                  }}
                >
                  {t("pages.media_manager.edit_first")}
                </Button>
              </Space>
              <List
                size="small"
                bordered
                dataSource={dirFiles}
                style={{ maxHeight: 420, overflow: "auto" }}
                renderItem={(item, idx) => (
                  <List.Item
                    actions={[
                      <Button
                        key="edit"
                        size="small"
                        onClick={() =>
                          setSelectedNode({
                            type: "file",
                            key: `file:${item.id}`,
                            name: item.title || item.file_id,
                            path: toLibraryDisplayRelativePath(item.file_path || "", selectedLibraryRoots),
                            mediaId: item.id,
                          })
                        }
                      >
                        {t("pages.media_manager.btn_edit")}
                      </Button>,
                      <Button
                        key="next"
                        size="small"
                        disabled={idx >= dirFiles.length - 1}
                        onClick={() => {
                          const next = dirFiles[idx + 1];
                          if (!next) return;
                          setSelectedNode({
                            type: "file",
                            key: `file:${next.id}`,
                            name: next.title || next.file_id,
                            path: toLibraryDisplayRelativePath(next.file_path || "", selectedLibraryRoots),
                            mediaId: next.id,
                          });
                        }}
                      >
                        {t("pages.media_manager.btn_next")}
                      </Button>,
                    ]}
                  >
                    <List.Item.Meta
                      title={item.title || item.file_id}
                      description={toLibraryDisplayRelativePath(item.file_path || "", selectedLibraryRoots)}
                    />
                  </List.Item>
                )}
              />
            </Card>
          ) : (
          <Card
            title={detail ? t("pages.media_manager.edit_modal_title", { id: detail.id, title: detail.title || t("pages.media_manager.default_untitled") }) : t("pages.media_manager.default_edit_title")}
            loading={loadingDetail}
            extra={
              <Space>
                <Button onClick={() => (selectedId ? void loadDetail(selectedId) : undefined)} disabled={!selectedId}>
                  {t("pages.media_manager.btn_reset")}
                </Button>
                <Button type="primary" onClick={() => void onSave()} loading={saving} disabled={!selectedId}>
                  {t("pages.media_manager.btn_save")}
                </Button>
              </Space>
            }
          >
            <Form form={form} layout="vertical">
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="title" label={t("pages.media_manager.field_title")}>
                    <Input />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="original_title" label={t("pages.media_manager.field_original_title")}>
                    <Input />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="status" label={t("pages.media_manager.field_status")}>
                    <Select
                      options={[
                        { value: "active", label: "active" },
                        { value: "inactive", label: "inactive" },
                        { value: "archived", label: "archived" },
                      ]}
                    />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="format" label={t("pages.media_manager.field_format")}>
                    <Input placeholder={t("pages.media_manager.format_placeholder")} />
                  </Form.Item>
                </Col>
              </Row>
              <Divider>{t("pages.media_manager.divider_scrape")}</Divider>
              <Form.Item name="overview" label={t("pages.media_manager.field_overview")}>
                <Input.TextArea rows={3} placeholder={t("pages.media_manager.overview_placeholder")} />
              </Form.Item>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item name="rating" label={t("pages.media_manager.field_rating")}>
                    <InputNumber min={0} max={10} step={0.1} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item name="year" label={t("pages.media_manager.field_year")}>
                    <InputNumber
                      min={1800}
                      max={2100}
                      placeholder={t("pages.media_manager.year_placeholder")}
                      style={{ width: "100%" }}
                    />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={12}>
                  <Form.Item name="directors" label={t("pages.media_manager.field_directors")}>
                    <Select {...tagSelectProps(t("pages.media_manager.directors_placeholder"))} />
                  </Form.Item>
                </Col>
                <Col span={12}>
                  <Form.Item name="countries" label={t("pages.media_manager.field_countries")}>
                    <Select {...tagSelectProps(t("pages.media_manager.countries_placeholder"))} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item name="genres" label={t("pages.media_manager.field_genres")}>
                <Select
                  {...tagSelectProps(t("pages.media_manager.genres_placeholder"))}
                  options={uniqueStrings([...genreOptions, ...(watchedGenres || [])]).map((g) => ({
                    value: g,
                    label: g,
                  }))}
                />
              </Form.Item>
              <Form.Item name="writers" label={t("pages.media_manager.field_writers")}>
                <Select {...tagSelectProps(t("pages.media_manager.writers_placeholder"))} />
              </Form.Item>
              <Form.Item name="producers" label={t("pages.media_manager.field_producers")}>
                <Select {...tagSelectProps(t("pages.media_manager.producers_placeholder"))} />
              </Form.Item>
              <Form.Item name="actors" label={t("pages.media_manager.field_actors")}>
                <Select {...tagSelectProps(t("pages.media_manager.actors_placeholder"))} />
              </Form.Item>
              <Row gutter={12}>
                <Col span={8}>
                  <Form.Item
                    name="poster"
                    label={
                      <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                        {t("pages.media_manager.field_poster")}
                        <Button
                          type="text"
                          size="small"
                          icon={<EditOutlined />}
                          aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                          onClick={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            if (!selectedId) return;
                            setPosterPickerOpen(true);
                          }}
                          disabled={!selectedId}
                        />
                      </span>
                    }
                  >
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item
                    name="backdrop"
                    label={
                      <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                        {t("pages.media_manager.field_backdrop")}
                        <Button
                          type="text"
                          size="small"
                          icon={<EditOutlined />}
                          aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                          onClick={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            if (!selectedId) return;
                            setBackdropPickerOpen(true);
                          }}
                          disabled={!selectedId}
                        />
                      </span>
                    }
                  >
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
                <Col span={8}>
                  <Form.Item
                    name="logo"
                    label={
                      <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
                        {t("pages.media_manager.field_logo")}
                        <Button
                          type="text"
                          size="small"
                          icon={<EditOutlined />}
                          aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                          onClick={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            if (!selectedId) return;
                            setLogoPickerOpen(true);
                          }}
                          disabled={!selectedId}
                        />
                      </span>
                    }
                  >
                    <Input placeholder="https://..." />
                  </Form.Item>
                </Col>
              </Row>
              <Row gutter={12}>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_poster_preview")}>
                    {posterPreview ? (
                      <Image src={posterPreview} alt="poster" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_backdrop_preview")}>
                    {backdropPreview ? (
                      <Image src={backdropPreview} alt="backdrop" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
                <Col span={8}>
                  <Card size="small" title={t("pages.media_manager.card_logo_preview")}>
                    {logoPreview ? (
                      <Image src={logoPreview} alt="logo" width="100%" />
                    ) : (
                      <Avatar shape="square" style={{ width: "100%", height: 120 }} />
                    )}
                  </Card>
                </Col>
              </Row>
              <Alert
                type="info"
                showIcon
                style={{ marginBottom: 12 }}
                message={t("pages.media_manager.auto_sync_msg")}
              />
              <Row gutter={12}>
                <Col span={6}>
                  <Form.Item name="duration" label={t("pages.media_manager.field_duration")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="width" label={t("pages.media_manager.field_width")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="height" label={t("pages.media_manager.field_height")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
                <Col span={6}>
                  <Form.Item name="bitrate" label={t("pages.media_manager.field_bitrate")}>
                    <InputNumber min={0} style={{ width: "100%" }} />
                  </Form.Item>
                </Col>
              </Row>
              <Form.Item
                name="meta_json"
                label={t("pages.media_manager.field_meta_json")}
                rules={[
                  {
                    validator: (_, value: string | undefined) => {
                      const raw = (value || "").trim();
                      if (!raw) return Promise.resolve();
                      try {
                        JSON.parse(raw);
                        return Promise.resolve();
                      } catch {
                        return Promise.reject(new Error(t("pages.media_manager.json_invalid_error")));
                      }
                    },
                  },
                ]}
              >
                <Input.TextArea rows={16} placeholder={t("pages.media_manager.meta_json_placeholder")} />
              </Form.Item>
            </Form>
          </Card>
          )}
        </Col>
      </Row>
      <MediaImagePickerDialog
        open={posterPickerOpen}
        onClose={() => setPosterPickerOpen(false)}
        mediaId={detail?.id}
        mediaTitle={detail?.title}
        mediaYear={detail?.year}
        kind="poster"
        currentUrl={posterPreview}
        autoFrameUrl={detail ? autoFrameForMedia(detail.id, "poster") : undefined}
        onConfirm={(url) => form.setFieldValue("poster", url)}
      />
      <MediaImagePickerDialog
        open={backdropPickerOpen}
        onClose={() => setBackdropPickerOpen(false)}
        mediaId={detail?.id}
        mediaTitle={detail?.title}
        mediaYear={detail?.year}
        kind="backdrop"
        currentUrl={backdropPreview}
        onConfirm={(url) => form.setFieldValue("backdrop", url)}
      />
      <MediaImagePickerDialog
        open={logoPickerOpen}
        onClose={() => setLogoPickerOpen(false)}
        mediaId={detail?.id}
        mediaTitle={detail?.title}
        mediaYear={detail?.year}
        kind="logo"
        currentUrl={logoPreview}
        onConfirm={(url) => form.setFieldValue("logo", url)}
      />
    </Space>
  );
}
