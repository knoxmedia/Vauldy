import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Library, MediaItem } from "../../api/client";
import { I18nProvider } from "../../i18n";
import { useAuthStore } from "../../store/auth";

const mocks = vi.hoisted(() => ({
  fetchLibraries: vi.fn(),
  fetchUserHistory: vi.fn(),
  loadRecent: vi.fn(),
}));

vi.mock("antd", () => ({
  Dropdown: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Modal: { confirm: vi.fn() },
  Popover: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Progress: () => null,
  Spin: () => <div role="status">loading</div>,
  Tag: ({ children, ...props }: React.HTMLAttributes<HTMLSpanElement>) => <span {...props}>{children}</span>,
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
vi.mock("../../components/mediaMenuItems", () => ({ buildMediaMenuItems: () => ({ items: [] }) }));
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

const library: Library = { id: 1, name: "Movies", type: "movie", path: "", auto_scan: 0, scraper: "", created_at: "" };

function recent(overrides: Partial<MediaItem> = {}): MediaItem {
  return {
    id: 9,
    library_id: 1,
    file_id: "f9",
    title: "Recovering movie",
    file_path: "movie.mkv",
    file_type: "video",
    duration: 0,
    width: 0,
    height: 0,
    format: "",
    status: "active",
    poster_url: "https://cdn.example/old.jpg",
    publication_state: "published",
    ingest_generation: 1,
    ...overrides,
  };
}

beforeEach(() => {
  vi.useRealTimers();
  vi.clearAllMocks();
  useAuthStore.setState({ token: "session", username: "alice" });
  mocks.fetchLibraries.mockResolvedValue([library]);
  mocks.fetchUserHistory.mockResolvedValue([]);
});

describe("Home poster publication recovery", () => {
  it("retries Home recent poster after generation changes", async () => {
    let generation = 1;
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => {
      onSection("movie", [recent({ ingest_generation: generation })]);
    });
    const { container } = render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await screen.findByText("Recovering movie");
    const img = container.querySelector('img[src="https://cdn.example/old.jpg"]') as HTMLImageElement;
    expect(img).toBeTruthy();
    fireEvent.error(img);
    expect(container.querySelector('img[src="https://cdn.example/old.jpg"]')).not.toBeInTheDocument();

    generation = 2;
    await new Promise((resolve) => window.setTimeout(resolve, 300));
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
      await Promise.resolve();
    });

    await waitFor(() => expect(container.querySelector('img[src="https://cdn.example/old.jpg"]')).toBeVisible());
  });

  it("restores Home history poster after mapped generation changes", async () => {
    let generation = 1;
    mocks.fetchUserHistory.mockResolvedValue([{
      media_id: 9, file_id: "f9", title: "Continue recovering", file_path: "movie.mkv",
      duration: 100, position: 10, update_at: "now",
    }]);
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => {
      onSection("movie", [recent({ ingest_generation: generation })]);
    });
    const { container } = render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await screen.findByText("Continue recovering");
    const historyImg = container.querySelectorAll('img[src="https://cdn.example/old.jpg"]')[0] as HTMLImageElement;
    fireEvent.error(historyImg);
    expect(screen.getByText("Continue recovering").closest('[role="button"]')?.querySelector("img")).toBeNull();

    generation = 2;
    await new Promise((resolve) => window.setTimeout(resolve, 300));
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
      await Promise.resolve();
    });

    await waitFor(() => expect(screen.getByText("Continue recovering").closest('[role="button"]')?.querySelector("img")).toBeVisible());
  });

  it("restores Home landscape fallback after generation changes", async () => {
    let generation = 1;
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => {
      onSection("tv", [recent({
        title: "Landscape recovering",
        poster_url: "https://cdn.example/poster.jpg",
        backdrop_url: "https://cdn.example/backdrop.jpg",
        ingest_generation: generation,
      })]);
    });
    const { container } = render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);
    await screen.findByText("Landscape recovering");
    const img = container.querySelector('img[src="https://cdn.example/backdrop.jpg"]') as HTMLImageElement;
    fireEvent.error(img);
    expect(img).toHaveAttribute("src", "https://cdn.example/poster.jpg");
    fireEvent.error(img);
    expect(container.querySelector('img[src="https://cdn.example/poster.jpg"]')).not.toBeInTheDocument();

    generation = 2;
    await new Promise((resolve) => window.setTimeout(resolve, 300));
    await act(async () => {
      window.dispatchEvent(new Event("focus"));
      await Promise.resolve();
    });

    await waitFor(() => expect(container.querySelector('img[src="https://cdn.example/backdrop.jpg"]')).toBeVisible());
  });

  it("renders degraded badge", async () => {
    mocks.loadRecent.mockImplementation(async (_libs, _sections, _signal, onSection) => {
      onSection("movie", [recent({ publication_state: "degraded" })]);
    });
    render(<I18nProvider locale="en"><MemoryRouter><HomePage /></MemoryRouter></I18nProvider>);

    expect(await screen.findByRole("status", { name: /degraded/i })).toBeInTheDocument();
  });
});
