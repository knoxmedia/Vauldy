import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode, useEffect } from "react";
import { MemoryRouter, useNavigate } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, MediaItem } from "../../api/client";

const mocks = vi.hoisted(() => ({
  fetchLibraries: vi.fn(),
  fetchMedia: vi.fn(),
  tvRequest: vi.fn(),
  musicRequest: vi.fn(),
  photoRequest: vi.fn(),
  documentRequest: vi.fn(),
  deleteMedia: vi.fn(),
  confirmOptions: undefined as undefined | { onOk?: () => Promise<void> },
  latestDropdownMenu: undefined as undefined | { items?: Array<{ key?: string }>; onClick?: (info: { key: string; domEvent: { stopPropagation: () => void } }) => void },
  mediaMenuExtras: [] as Array<Record<string, unknown>>,
  childSignals: {} as Record<string, AbortSignal | undefined>,
  emptyHandlers: {} as Record<string, (() => void) | undefined>,
  t: (key: string) => key,
}));

vi.mock("antd", () => ({
  Button: ({ children, onClick, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) => <button onClick={onClick} {...props}>{children}</button>,
  Checkbox: () => null,
  Dropdown: ({ children, menu }: { children?: React.ReactNode; menu?: typeof mocks.latestDropdownMenu }) => {
    if (menu?.items?.some((item) => item?.key === "delete")) mocks.latestDropdownMenu = menu;
    return <>{children}</>;
  },
  Empty: ({ description }: { description?: React.ReactNode }) => <div>{description}</div>,
  Modal: { confirm: (options: { onOk?: () => Promise<void> }) => { mocks.confirmOptions = options; } },
  Popover: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Select: () => null,
  Space: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Spin: () => <div role="status">loading</div>,
  Pagination: () => null,
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));
vi.mock("@ant-design/icons", () => ({
  AppstoreOutlined: () => null, ArrowDownOutlined: () => null, ArrowUpOutlined: () => null, BarsOutlined: () => null,
  CaretDownOutlined: () => null, CaretRightOutlined: () => null, CaretUpOutlined: () => null, CheckCircleOutlined: () => null,
  CheckOutlined: () => null, CloseOutlined: () => null, DownOutlined: () => null, EditOutlined: () => null,
  EllipsisOutlined: () => null, PictureOutlined: () => null, PlayCircleOutlined: () => null, SlidersOutlined: () => null,
  TableOutlined: () => null, UnorderedListOutlined: () => null, UpOutlined: () => null,
}));vi.mock("../../enterprise", () => ({ useLicenseStatus: () => ({ status: undefined }) }));
vi.mock("../../components/AddToFavoriteFolderPickerModal", () => ({ default: () => null }));
vi.mock("../../components/AddToPlaylistModal", () => ({ default: () => null }));
vi.mock("../../components/MediaMatchModal", () => ({ default: () => null }));
vi.mock("../../components/VideoOptimizationModal", () => ({ default: () => null }));
vi.mock("../../components/mediaMenuItems", () => ({ buildMediaMenuItems: (_item: unknown, _nav: unknown, extras: Record<string, unknown>) => {
  mocks.mediaMenuExtras.push(extras);
  return { items: [] };
} }));
vi.mock("../../lib/useFavoriteFolderMenuRecents", () => ({ useFavoriteFolderMenuRecents: () => ({ recentFavoriteFolders: [], rememberFolderMenuAdded: vi.fn() }) }));
vi.mock("../../lib/recentPlaylists", () => ({ readRecentPlaylists: () => [], rememberPlaylistAdded: vi.fn() }));
vi.mock("../../i18n", () => ({ useT: () => mocks.t }));

function specializedMock(kind: "tv" | "music" | "photo" | "document") {
  return function Specialized({ libraryId, onEmpty, signal }: { libraryId: number; onEmpty?: () => void; signal?: AbortSignal }) {
    useEffect(() => {
      const requestMock = kind === "tv" ? mocks.tvRequest : kind === "music" ? mocks.musicRequest : kind === "photo" ? mocks.photoRequest : mocks.documentRequest;
      requestMock(libraryId);
      mocks.childSignals[kind] = signal;
      mocks.emptyHandlers[kind] = onEmpty;
      return () => { if (mocks.emptyHandlers[kind] === onEmpty) delete mocks.emptyHandlers[kind]; };
    }, [libraryId, onEmpty, signal]);
    return <div>{kind}-browse-{libraryId}</div>;
  };
}
vi.mock("../SeriesBrowse", () => ({ default: specializedMock("tv") }));
vi.mock("../MusicBrowse", () => ({ default: specializedMock("music") }));
vi.mock("../PhotoBrowse", () => ({ default: specializedMock("photo") }));
vi.mock("../DocumentBrowse", () => ({ default: specializedMock("document") }));
vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, fetchLibraries: mocks.fetchLibraries, fetchMedia: mocks.fetchMedia, deleteMedia: mocks.deleteMedia };
});

import BrowsePage from "../Browse";

const library = (id: number, type: string): Library => ({ id, type, name: `${type}-${id}`, path: "", auto_scan: 0, scraper: "", created_at: "" });
const media = (id: number, title: string): MediaItem => ({ id, library_id: id, file_id: String(id), title, file_path: "", file_type: "video", duration: 0, width: 0, height: 0, format: "", status: "active" } as MediaItem);

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function RouteControls() {
  const navigate = useNavigate();
  return <>
    <button onClick={() => navigate("/browse?library_id=2")}>library-2</button>
    <button onClick={() => navigate("/browse?library_id=1&sort=recent")}>recent-sort</button>
    <button onClick={() => navigate("/browse?library_id=2&sort=recent")}>library-2-recent</button>
    <BrowsePage />
  </>;
}

function mount(url: string, controls = false) {
  return render(<MemoryRouter initialEntries={[url]}>{controls ? <RouteControls /> : <BrowsePage />}</MemoryRouter>);
}

beforeEach(() => {
  vi.clearAllMocks();
  for (const key of Object.keys(mocks.emptyHandlers)) delete mocks.emptyHandlers[key];
  for (const key of Object.keys(mocks.childSignals)) delete mocks.childSignals[key];
  mocks.fetchMedia.mockResolvedValue([]);
  mocks.deleteMedia.mockResolvedValue(undefined);
  mocks.confirmOptions = undefined;
  mocks.latestDropdownMenu = undefined;
  mocks.mediaMenuExtras.length = 0;
});

describe("Browse request routing", () => {
  it.each([
    ["tv", "tvRequest"],
    ["music", "musicRequest"],
    ["photo", "photoRequest"],
    ["document", "documentRequest"],
  ] as const)("uses only the %s specialized route after resolution", async (type, request) => {
    mocks.fetchLibraries.mockResolvedValue([library(1, type)]);
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks[request]).toHaveBeenCalledWith(1));
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it("issues no data request while library resolution is pending", async () => {
    mocks.fetchLibraries.mockReturnValue(new Promise(() => undefined));
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
    expect(mocks.tvRequest).not.toHaveBeenCalled();
  });

  it.each(["tv", "photo"] as const)("allows %s flat fallback only after current child onEmpty", async (type) => {
    mocks.fetchLibraries.mockResolvedValue([library(1, type)]);
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks[`${type}Request`]).toHaveBeenCalledWith(1));
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
    act(() => mocks.emptyHandlers[type]?.());
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    expect(mocks.fetchMedia).toHaveBeenCalledWith(1, undefined, expect.any(AbortSignal));
  });

  it("keeps music specialized after its child reports empty", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "music")]);
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.musicRequest).toHaveBeenCalledWith(1));
    expect(mocks.emptyHandlers.music).toBeUndefined();
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it("never falls document back to generic media", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "document")]);
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.documentRequest).toHaveBeenCalledWith(1));
    expect(mocks.emptyHandlers.document).toBeUndefined();
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it.each(["abc", "0", "-1", "1.5"])("rejects malformed library id %s without API requests", async (value) => {
    mount(`/browse?library_id=${value}`);
    expect(await screen.findByRole("alert")).toHaveTextContent("common.loading_failed");
    expect(mocks.fetchLibraries).not.toHaveBeenCalled();
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it("treats a missing library as resolution error instead of generic fallback", async () => {
    mocks.fetchLibraries.mockResolvedValue([]);
    mount("/browse?library_id=99");
    expect(await screen.findByRole("alert")).toHaveTextContent("common.loading_failed");
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it("shows a retryable resolution error without generic fallback", async () => {
    mocks.fetchLibraries.mockRejectedValueOnce(new Error("resolution failed")).mockResolvedValueOnce([library(1, "movie")]);
    mount("/browse?library_id=1");
    expect(await screen.findByRole("alert")).toHaveTextContent("resolution failed");
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "common.retry" }));
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
  });

  it.each([
    ["tv", "tvRequest"], ["music", "musicRequest"], ["photo", "photoRequest"], ["document", "documentRequest"],
  ] as const)("aborts the %s specialized request signal on URL switch", async (type, request) => {
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, type)]).mockResolvedValueOnce([library(2, "movie")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks[request]).toHaveBeenCalledWith(1));
    const signal = mocks.childSignals[type];
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(signal?.aborted).toBe(false);
    fireEvent.click(screen.getByText("library-2"));
    expect(signal?.aborted).toBe(true);
  });

  it("aborts the specialized request signal on unmount", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "tv")]);
    const mounted = mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.tvRequest).toHaveBeenCalledWith(1));
    const signal = mocks.childSignals.tv;
    mounted.unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("renders a neutral loading state immediately on specialized URL switch", async () => {
    const nextLibraries = deferred<Library[]>();
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "tv")]).mockReturnValueOnce(nextLibraries.promise);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.tvRequest).toHaveBeenCalledWith(1));
    fireEvent.click(screen.getByText("library-2"));
    expect(screen.getByRole("status")).toBeInTheDocument();
    expect(screen.queryByText("tv-browse-1")).not.toBeInTheDocument();
    expect(mocks.tvRequest).not.toHaveBeenCalledWith(2);
    expect(mocks.documentRequest).not.toHaveBeenCalled();
  });

  it.each(["", "unknown", "other"])("rejects unsupported resolved type %s without generic media", async (type) => {
    mocks.fetchLibraries.mockResolvedValue([library(1, type)]);
    mount("/browse?library_id=1");
    expect(await screen.findByRole("alert")).toHaveTextContent("common.loading_failed");
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
    expect(mocks.tvRequest).not.toHaveBeenCalled();
    expect(mocks.musicRequest).not.toHaveBeenCalled();
    expect(mocks.photoRequest).not.toHaveBeenCalled();
    expect(mocks.documentRequest).not.toHaveBeenCalled();
  });

  it("aborts old resolution and ignores its deferred result after URL switch", async () => {
    const old = deferred<Library[]>();
    mocks.fetchLibraries.mockReturnValueOnce(old.promise).mockResolvedValueOnce([library(2, "document")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(1));
    const oldSignal = mocks.fetchLibraries.mock.calls[0]![0] as AbortSignal;
    fireEvent.click(screen.getByText("library-2"));
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(2));
    expect(oldSignal.aborted).toBe(true);
    await waitFor(() => expect(mocks.documentRequest).toHaveBeenCalledWith(2));
    await act(async () => old.resolve([library(1, "movie")]));
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
    expect(screen.getByText("document-browse-2")).toBeInTheDocument();
  });

  it("ignores onEmpty from an old specialized generation", async () => {
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "tv")]).mockResolvedValueOnce([library(2, "document")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.tvRequest).toHaveBeenCalledWith(1));
    const staleOnEmpty = mocks.emptyHandlers.tv!;
    fireEvent.click(screen.getByText("library-2"));
    await waitFor(() => expect(mocks.documentRequest).toHaveBeenCalledWith(2));
    act(() => staleOnEmpty());
    expect(mocks.fetchMedia).not.toHaveBeenCalled();
  });

  it("routes anime to generic media exactly once without SeriesBrowse", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "anime")]);
    mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    expect(mocks.fetchMedia).toHaveBeenCalledWith(1, undefined, expect.any(AbortSignal));
    expect(mocks.tvRequest).not.toHaveBeenCalled();
  });

  it("loads movie generic media once after ready and once for a sort URL change", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "movie")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    expect(mocks.fetchMedia).toHaveBeenLastCalledWith(1, undefined, expect.any(AbortSignal));
    fireEvent.click(screen.getByText("recent-sort"));
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(2));
    expect(mocks.fetchMedia).toHaveBeenLastCalledWith(1, { sort: "created_desc", limit: 200 }, expect.any(AbortSignal));
  });

  it("does not double generic media requests in development StrictMode", async () => {
    mocks.fetchLibraries.mockResolvedValue([library(1, "movie")]);
    render(<StrictMode><MemoryRouter initialEntries={["/browse?library_id=1"]}><BrowsePage /></MemoryRouter></StrictMode>);
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
  });

  it("does not double all-media requests in development StrictMode", async () => {
    render(<StrictMode><MemoryRouter initialEntries={["/browse?q=needle"]}><BrowsePage /></MemoryRouter></StrictMode>);
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    expect(mocks.fetchLibraries).not.toHaveBeenCalled();
  });

  it("all-media route skips library resolution and loads generic media once", async () => {
    mount("/browse?q=needle");
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    expect(mocks.fetchLibraries).not.toHaveBeenCalled();
    expect(mocks.fetchMedia).toHaveBeenCalledWith(undefined, undefined, expect.any(AbortSignal));
  });

  it("resets specialized fallback on URL switch", async () => {
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "tv")]).mockResolvedValueOnce([library(2, "tv")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.tvRequest).toHaveBeenCalledWith(1));
    act(() => mocks.emptyHandlers.tv?.());
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByText("library-2"));
    await waitFor(() => expect(mocks.tvRequest).toHaveBeenCalledWith(2));
    expect(mocks.fetchMedia).toHaveBeenCalledTimes(1);
  });

  it("aborts generic media on unmount and ignores its result", async () => {
    const pending = deferred<MediaItem[]>();
    mocks.fetchLibraries.mockResolvedValue([library(1, "movie")]);
    mocks.fetchMedia.mockReturnValue(pending.promise);
    const mounted = mount("/browse?library_id=1");
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    const signal = mocks.fetchMedia.mock.calls[0]![2] as AbortSignal;
    mounted.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => pending.resolve([media(1, "late-row")]));
    expect(screen.queryByText("late-row")).not.toBeInTheDocument();
  });

  it("ignores deferred bulk delete completion after navigating to a new route", async () => {
    const deletion = deferred<void>();
    mocks.deleteMedia.mockReturnValueOnce(deletion.promise);
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "movie")]).mockResolvedValueOnce([library(2, "movie")]);
    mocks.fetchMedia.mockResolvedValueOnce([media(1, "old-row")]).mockResolvedValueOnce([media(2, "new-row")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(screen.getByText("old-row")).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText("pages.browse.aria_select"));
    await waitFor(() => expect(mocks.latestDropdownMenu).toBeDefined());
    act(() => mocks.latestDropdownMenu?.onClick?.({ key: "delete", domEvent: { stopPropagation() {} } }));
    const deleteCompletion = mocks.confirmOptions?.onOk?.();
    expect(mocks.deleteMedia).toHaveBeenCalledWith(1);
    fireEvent.click(screen.getByText("library-2"));
    await waitFor(() => expect(screen.getByText("new-row")).toBeInTheDocument());
    const currentSignal = mocks.fetchMedia.mock.calls[1]![2] as AbortSignal;
    await act(async () => deletion.resolve());
    await deleteCompletion;
    expect(mocks.fetchMedia).toHaveBeenCalledTimes(2);
    expect(currentSignal.aborted).toBe(false);
    expect(screen.getByText("new-row")).toBeInTheDocument();
  });

  it("ignores stale menu reload callback without aborting the current route", async () => {
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "movie")]).mockResolvedValueOnce([library(2, "movie")]);
    mocks.fetchMedia.mockResolvedValueOnce([media(1, "old-row")]).mockResolvedValueOnce([media(2, "new-row")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(screen.getByText("old-row")).toBeInTheDocument());
    const staleReload = mocks.mediaMenuExtras.at(-1)?.afterDelete as (() => Promise<void>) | undefined;
    expect(staleReload).toEqual(expect.any(Function));
    fireEvent.click(screen.getByText("library-2"));
    await waitFor(() => expect(screen.getByText("new-row")).toBeInTheDocument());
    const currentSignal = mocks.fetchMedia.mock.calls[1]![2] as AbortSignal;
    await staleReload?.();
    expect(mocks.fetchMedia).toHaveBeenCalledTimes(2);
    expect(currentSignal.aborted).toBe(false);
    expect(screen.getByText("new-row")).toBeInTheDocument();
  });

  it("aborts generic media and clears stale rows on route change", async () => {
    const oldMedia = deferred<MediaItem[]>();
    mocks.fetchLibraries.mockResolvedValueOnce([library(1, "movie")]).mockResolvedValueOnce([library(2, "movie")]);
    mocks.fetchMedia.mockReturnValueOnce(oldMedia.promise).mockResolvedValueOnce([media(2, "new-row")]);
    mount("/browse?library_id=1", true);
    await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalledTimes(1));
    const oldSignal = mocks.fetchMedia.mock.calls[0]![2] as AbortSignal;
    fireEvent.click(screen.getByText("library-2"));
    expect(oldSignal.aborted).toBe(true);
    expect(screen.queryByText("old-row")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("new-row")).toBeInTheDocument());
    await act(async () => oldMedia.resolve([media(1, "old-row")]));
    expect(screen.queryByText("old-row")).not.toBeInTheDocument();
    expect(screen.getByText("new-row")).toBeInTheDocument();
  });
});
