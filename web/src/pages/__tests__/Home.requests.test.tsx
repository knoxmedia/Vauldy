import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, MediaItem } from "../../api/client";
import { useAuthStore } from "../../store/auth";
import { I18nProvider, useI18n } from "../../i18n";

const mocks = vi.hoisted(() => ({
  fetchLibraries: vi.fn(),
  buildMediaMenuItems: vi.fn((..._args: unknown[]) => ({ items: [] })),
  fetchUserHistory: vi.fn(),
  loadRecent: vi.fn(),
}));

vi.mock("antd", () => ({
  Dropdown: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Modal: { confirm: vi.fn() },
  Popover: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Progress: () => null,
  Spin: () => <div role="status">loading</div>,
  Tag: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Typography: { Title: ({ children }: { children?: React.ReactNode }) => <h2>{children}</h2> },
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
}));
vi.mock("@ant-design/icons", () => ({
  CaretRightOutlined: () => null, CheckCircleOutlined: () => null, CheckOutlined: () => null, CloseOutlined: () => null,
  EditOutlined: () => null, EllipsisOutlined: () => null, FileImageOutlined: () => null, LeftOutlined: () => null,
  MoreOutlined: () => null, PlayCircleOutlined: () => null, RightOutlined: () => null, UnorderedListOutlined: () => null,
}));
vi.mock("../../components/AddToFavoriteFolderPickerModal", () => ({ default: () => null }));
vi.mock("../../components/AddToPlaylistModal", () => ({ default: () => null }));
vi.mock("../../components/MusicPosterPlaceholderIcon", () => ({ default: () => null }));
vi.mock("../../components/PhotoLightbox", () => ({ default: () => null }));
vi.mock("../../components/mediaMenuItems", () => ({ buildMediaMenuItems: mocks.buildMediaMenuItems }));
vi.mock("../../lib/useFavoriteFolderMenuRecents", () => ({ useFavoriteFolderMenuRecents: () => ({ recentFavoriteFolders: [], rememberFolderMenuAdded: vi.fn() }) }));
vi.mock("../../lib/recentPlaylists", () => ({ readRecentPlaylists: () => [], rememberPlaylistAdded: vi.fn() }));
vi.mock("../../lib/albumPlayback", () => ({ mediaItemsToMusicQueue: () => [] }));
vi.mock("../../store/musicPlayer", () => ({ useMusicPlayerStore: { getState: () => ({ playQueue: vi.fn() }) } }));
vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, fetchLibraries: mocks.fetchLibraries, fetchUserHistory: mocks.fetchUserHistory };
});
vi.mock("../../lib/homeRecentSections", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/homeRecentSections")>();
  return { ...actual, loadHomeRecentBySection: mocks.loadRecent };
});

import HomePage from "../Home";

function LanguageSwitch() {
  const { setLocale } = useI18n();
  return <button onClick={() => setLocale("zh-CN")}>switch language</button>;
}

const library: Library = { id: 1, name: "Movies", type: "movie", path: "", auto_scan: 0, scraper: "", created_at: "" };
const recent = { id: 1, library_id: 1, file_id: "f", title: "Current title", file_path: "p", file_type: "video", duration: 0, width: 0, height: 0, format: "", status: "active" } as MediaItem;

afterEach(() => vi.useRealTimers());

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({ token: "session-a", username: "alice" });
});

describe("HomePage request effect", () => {
  it("starts each source once and hides spinner before deferred recent finishes", async () => {
    mocks.fetchLibraries.mockResolvedValue([library]);
    mocks.fetchUserHistory.mockResolvedValue([]);
    mocks.loadRecent.mockImplementation(() => new Promise<void>(() => undefined));
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
    expect(mocks.fetchLibraries).toHaveBeenCalledTimes(1);
    expect(mocks.fetchUserHistory).toHaveBeenCalledTimes(1);
    expect(mocks.loadRecent).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Movies")).toBeInTheDocument();
  });

  it("restarts and aborts the old generation when session identity changes", async () => {
    const oldLibraries = new Promise<Library[]>(() => undefined);
    mocks.fetchLibraries.mockReturnValueOnce(oldLibraries).mockResolvedValueOnce([library]);
    mocks.fetchUserHistory.mockReturnValueOnce(new Promise(() => undefined)).mockResolvedValueOnce([]);
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => onSection("movie", [recent]));
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    const oldSignal = mocks.fetchLibraries.mock.calls[0]![0] as AbortSignal;
    useAuthStore.setState({ token: "session-b", username: "bob" });
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(2));
    expect(oldSignal.aborted).toBe(true);
    await waitFor(() => expect(screen.getByText("Current title")).toBeInTheDocument());
  });

  it("keeps rendered shelves and never restores the spinner during recent refresh", async () => {
    vi.useFakeTimers();
    let sectionCallback: ((key: string, items: MediaItem[]) => void) | undefined;
    mocks.fetchLibraries.mockResolvedValue([library]);
    mocks.fetchUserHistory.mockResolvedValue([]);
    mocks.loadRecent.mockImplementationOnce(async (_libs, _sections, _signal, onSection) => {
      onSection("movie", [recent]);
    }).mockImplementationOnce((_libs, _sections, _signal, onSection) => {
      sectionCallback = onSection;
      return new Promise<void>(() => undefined);
    });
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await vi.waitFor(() => expect(screen.getByText("Current title")).toBeInTheDocument());
    await vi.advanceTimersByTimeAsync(10_000);
    expect(mocks.loadRecent).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Current title")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    sectionCallback?.("movie", [{ ...recent, title: "Refreshed title" }]);
  });

  it("pauses polling while hidden and refreshes once when visible", async () => {
    vi.useFakeTimers();
    let hidden = false;
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    mocks.fetchLibraries.mockResolvedValue([library]);
    mocks.fetchUserHistory.mockResolvedValue([]);
    mocks.loadRecent.mockResolvedValue(undefined);
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await vi.waitFor(() => expect(screen.queryByRole("status")).not.toBeInTheDocument());
    hidden = true;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.advanceTimersByTimeAsync(30_000);
    expect(mocks.fetchLibraries).toHaveBeenCalledTimes(1);
    expect(mocks.loadRecent).toHaveBeenCalledTimes(1);
    hidden = false;
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(2));
    expect(mocks.loadRecent).toHaveBeenCalledTimes(2);
  });
  it("clears committed user shelves before painting the next session", async () => {
    mocks.fetchLibraries.mockResolvedValueOnce([library]).mockImplementationOnce(() => new Promise<Library[]>(() => undefined));
    mocks.fetchUserHistory.mockResolvedValueOnce([]).mockImplementationOnce(() => new Promise(() => undefined));
    mocks.loadRecent.mockImplementationOnce(async (_libs, _sections, _signal, onSection) => onSection("movie", [recent]));
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await waitFor(() => expect(screen.getByText("Current title")).toBeInTheDocument());
    act(() => useAuthStore.setState({ token: "session-b", username: "bob" }));
    expect(screen.queryByText("Current title")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });
  it("routes menu watched, delete, and continue-removal mutations through coordinator refresh", async () => {
    const historyItem = { media_id: 1, title: "Continue item", file_id: "f", file_path: "p", duration: 10, position: 1, update_at: "now" };
    mocks.fetchLibraries.mockResolvedValue([library]);
    mocks.fetchUserHistory.mockResolvedValue([historyItem]);
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => onSection("movie", [recent]));
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await waitFor(() => expect(screen.getByText("Current title")).toBeInTheDocument());
    const extras = mocks.buildMediaMenuItems.mock.calls.map((call) => call[2] as Record<string, unknown>).filter(Boolean);
    const continueExtra = extras.find((extra) => extra.fromContinueWatching);
    const recentExtra = extras.find((extra) => !extra.fromContinueWatching);
    expect(recentExtra?.afterToggleWatched).toEqual(expect.any(Function));
    expect(recentExtra?.afterDelete).toEqual(expect.any(Function));
    expect(continueExtra?.onRemoveFromContinueWatching).toEqual(expect.any(Function));
  });
  it("restarts requests on language change without hiding committed shelves or showing spinner", async () => {
    mocks.fetchLibraries.mockResolvedValue([library]);
    mocks.fetchUserHistory.mockResolvedValue([]);
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => onSection("movie", [recent]));
    render(<I18nProvider locale="en"><LanguageSwitch /><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await waitFor(() => expect(screen.getByText("Current title")).toBeInTheDocument());
    const firstSignal = mocks.fetchLibraries.mock.calls[0]![0] as AbortSignal;
    act(() => screen.getByText("switch language").click());
    expect(screen.getByText("Current title")).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.fetchLibraries).toHaveBeenCalledTimes(2));
    expect(firstSignal.aborted).toBe(true);
  });});







\n