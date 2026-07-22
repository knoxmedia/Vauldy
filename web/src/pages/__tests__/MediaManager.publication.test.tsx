import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminMediaItem, AdminMediaPage, Library, MediaDetail } from "../../api/client";
import { I18nProvider } from "../../i18n";

const mocks = vi.hoisted(() => ({
  fetchLibraries: vi.fn(),
  fetchMedia: vi.fn(),
  fetchAdminMedia: vi.fn(),
  fetchMediaDetail: vi.fn(),
  fetchMediaPersons: vi.fn(),
  messageError: vi.fn(),
}));

vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, ...mocks };
});
vi.mock("antd", async (importOriginal) => {
  const actual = await importOriginal<typeof import("antd")>();
  return { ...actual, message: { ...actual.message, error: mocks.messageError } };
});
vi.mock("../../components/MediaImagePickerDialog", () => ({ default: () => null, autoFrameForMedia: () => "" }));

import MediaManagerPage from "../MediaManager";

const library = (id: number, name: string): Library => ({ id, name, type: "movie", path: "", auto_scan: 0, scraper: "", created_at: "" });
function adminMedia(id: number, state: AdminMediaItem["publication_state"], title: string = state, libraryId = 1): AdminMediaItem {
  return {
    id, library_id: libraryId, file_id: `f${id}`, title, file_path: `${title}.mkv`, file_type: "video",
    duration: 0, width: 0, height: 0, format: "", status: "active", publication_state: state, ingest_generation: 1,
  };
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => cleanup());

beforeEach(() => {
  vi.clearAllMocks();
  mocks.fetchLibraries.mockReset();
  mocks.fetchMedia.mockReset();
  mocks.fetchAdminMedia.mockReset();
  mocks.fetchMediaDetail.mockReset();
  mocks.fetchMediaPersons.mockReset();
  mocks.messageError.mockReset();
  mocks.fetchLibraries.mockResolvedValue([library(1, "Library A")]);
  mocks.fetchAdminMedia.mockResolvedValue({ items: [], has_more: false });
  mocks.fetchMedia.mockResolvedValue([]);
  mocks.fetchMediaDetail.mockImplementation(async (id: number) => ({ ...adminMedia(id, "published", `Detail ${id}`), meta_json: "{}" }) as MediaDetail);
  mocks.fetchMediaPersons.mockResolvedValue({ items: [], resolved: [] });
});

describe("MediaManager publication diagnostics", () => {
  it("loads one admin page at a time and appends by cursor", async () => {
    mocks.fetchAdminMedia
      .mockResolvedValueOnce({ items: [adminMedia(1, "processing", "Page one")], next_cursor: "page-2", has_more: true })
      .mockResolvedValueOnce({ items: [adminMedia(2, "degraded", "Page two")], next_cursor: "page-3", has_more: true })
      .mockResolvedValueOnce({ items: [adminMedia(3, "failed", "Page three")], has_more: false });
    const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
    const ui = within(view.container);

    await waitFor(() => expect(view.container).toHaveTextContent("Page one"), { timeout: 10_000 });
    expect(mocks.fetchAdminMedia).toHaveBeenCalledTimes(1);
    fireEvent.click(ui.getByRole("button", { name: /load more/i }));
    await waitFor(() => expect(view.container).toHaveTextContent("Page two"), { timeout: 10_000 });
    expect(mocks.fetchAdminMedia).toHaveBeenCalledTimes(2);
    expect(mocks.fetchAdminMedia).toHaveBeenNthCalledWith(2, expect.objectContaining({ limit: 500, cursor: "page-2" }), expect.any(AbortSignal));
    expect(ui.queryByText(/Page three$/)).not.toBeInTheDocument();
    const secondLoadMore = ui.getByRole("button", { name: /load more/i });
    await waitFor(() => expect(secondLoadMore).not.toBeDisabled());
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 50)); });
    fireEvent.click(secondLoadMore);
    await waitFor(() => expect(mocks.fetchAdminMedia).toHaveBeenCalledTimes(3), { timeout: 10_000 });
    await waitFor(() => expect(view.container).toHaveTextContent("Page three"), { timeout: 10_000 });
    expect(mocks.fetchAdminMedia).toHaveBeenCalledTimes(3);
    expect(ui.queryByRole("button", { name: /load more/i })).not.toBeInTheDocument();
  }, 15_000);

  it("deduplicates items and stops when the server repeats a cursor", async () => {
    const item = adminMedia(1, "processing", "Only once");
    mocks.fetchAdminMedia
      .mockResolvedValueOnce({ items: [item], next_cursor: "same", has_more: true })
      .mockResolvedValueOnce({ items: [item], next_cursor: "same", has_more: true });
    const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
    const ui = within(view.container);
    await waitFor(() => expect(view.container).toHaveTextContent("Only once"), { timeout: 10_000 });
    fireEvent.click(screen.getByRole("button", { name: /load more/i }));
    await waitFor(() => expect(mocks.fetchAdminMedia).toHaveBeenCalledTimes(2));
    expect(ui.getAllByText(/Only once$/)).toHaveLength(2);
    expect(ui.queryByRole("button", { name: /load more/i })).not.toBeInTheDocument();
  }, 15_000);

  it("clears and isolates media state when switching libraries", async () => {
    const slowA = deferred<AdminMediaPage>();
    mocks.fetchLibraries.mockResolvedValue([library(1, "Library A"), library(2, "Library B")]);
    mocks.fetchAdminMedia.mockImplementation(({ library_id }: { library_id?: number }) =>
      library_id === 1 ? slowA.promise : Promise.resolve({ items: [adminMedia(20, "degraded", "Library B item", 2)], has_more: false }),
    );
    const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
    const ui = within(view.container);
    await waitFor(() => expect(mocks.fetchAdminMedia).toHaveBeenCalledWith(expect.objectContaining({ library_id: 1 }), expect.any(AbortSignal)));

    const librarySelect = ui.getAllByRole("combobox")[0];
    fireEvent.mouseDown(librarySelect);
    fireEvent.click(await screen.findByText("Library B", { selector: ".ant-select-item-option-content" }));
    await waitFor(() => expect(librarySelect.closest(".ant-select")).toHaveTextContent("Library B"));
    expect(ui.queryByText(/Library A item$/)).not.toBeInTheDocument();
    await waitFor(() => expect(view.container).toHaveTextContent("Library B item"), { timeout: 10_000 });
    await act(async () => slowA.resolve({ items: [adminMedia(10, "processing", "Library A item", 1)], has_more: false }));
    expect(ui.queryByText(/Library A item$/)).not.toBeInTheDocument();
    expect(ui.getAllByText(/Library B item$/).length).toBeGreaterThan(0);
  }, 15_000);

  it("silently ignores an aborted stale detail request", async () => {
    const alpha = adminMedia(1, "published", "Alpha");
    const beta = adminMedia(2, "published", "Beta");
    mocks.fetchAdminMedia.mockResolvedValue({ items: [alpha, beta], has_more: false });
    mocks.fetchMediaDetail.mockImplementation((id: number, signal?: AbortSignal) => {
      if (id === 2) return Promise.resolve({ ...beta, title: "Beta detail", meta_json: "{}" }) as Promise<MediaDetail>;
      return new Promise<MediaDetail>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), { once: true });
      });
    });
    const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
    const ui = within(view.container);
    await waitFor(() => expect(view.container).toHaveTextContent("Beta"), { timeout: 10_000 });
    fireEvent.click(ui.getAllByText(/Beta$/).at(-1)!);
    await waitFor(() => expect(ui.getByLabelText("Title")).toHaveValue("Beta detail"));
    expect(mocks.messageError).not.toHaveBeenCalled();
  }, 15_000);

  it("keeps the newest selected detail when an older request resolves", async () => {
    const slowA = deferred<MediaDetail>();
    const alpha = adminMedia(1, "published", "Alpha");
    const beta = adminMedia(2, "published", "Beta");
    mocks.fetchAdminMedia.mockResolvedValue({ items: [alpha, beta], has_more: false });
    mocks.fetchMediaDetail.mockImplementation((id: number) => id === 1
      ? slowA.promise
      : Promise.resolve({ ...beta, title: "Beta detail", meta_json: "{}" }) as Promise<MediaDetail>);
    const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
    const ui = within(view.container);
    await waitFor(() => expect(view.container).toHaveTextContent("Beta"), { timeout: 10_000 });
    fireEvent.click(ui.getAllByText(/Beta$/).at(-1)!);
    await waitFor(() => expect(ui.getByLabelText("Title")).toHaveValue("Beta detail"));
    await act(async () => slowA.resolve({ ...alpha, title: "Alpha stale", meta_json: "{}" } as MediaDetail));
    expect(ui.getByLabelText("Title")).toHaveValue("Beta detail");
  }, 15_000);
});
