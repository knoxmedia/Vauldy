import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library } from "../../api/client";
import { I18nProvider } from "../../i18n";

const mocks = vi.hoisted(() => ({ fetch: vi.fn(), update: vi.fn(), create: vi.fn() }));
vi.mock("../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../api/client")>();
  return { ...actual, fetchLibrariesWithCapabilities: mocks.fetch, updateLibrary: mocks.update, createLibrary: mocks.create, scanLibrary: vi.fn(), cancelScanTask: vi.fn(), deleteLibrary: vi.fn() };
});
import LibraryPage from "../Library";

const result = (items: Library[]) => ({ items, drmCapabilities: { widevine_enabled: true, powerdrm_enabled: true }, encryptedAssetsConfig: {} });
const library = (overrides: Partial<Library> = {}): Library => ({
  id: 7, name: "Anime Library", type: "anime", path: "E:/anime", folders: ["E:/anime"], auto_scan: 0, scraper: "tmdb", created_at: "",
  preview_extract: 0, subtitle_extract: 0, atrack_extract: 0, subtitle_recognize: 0, keyframe_extract: 1, ai_analysis: 1,
  processing_options: {
    explicit: { preview: false, subtitle_extract: false, atrack_extract: false, subtitle_recognize: false, keyframe_extract: true, ai_analysis: true },
    effective: { preview: false, subtitle_extract: true, atrack_extract: true, subtitle_recognize: true, keyframe_extract: true, ai_analysis: true },
    provenance: { explicit: ["ai_analysis", "keyframe_extract"], dependency_added: ["atrack_extract", "subtitle_extract", "subtitle_recognize"] },
  }, ...overrides,
});
function renderPage(item = library()) { mocks.fetch.mockResolvedValue(result([item])); return render(<I18nProvider locale="en"><LibraryPage /></I18nProvider>); }
function switchFor(label: string): HTMLElement { const item = screen.getByText(label).closest(".ant-form-item"); if (!item) throw new Error(`No form item for ${label}`); return within(item as HTMLElement).getByRole("switch"); }
async function openEdit(item = library()) { renderPage(item); await screen.findByText("Anime Library"); fireEvent.click(screen.getByRole("button", { name: "Edit" })); }
afterEach(() => cleanup());
beforeEach(() => { vi.clearAllMocks(); mocks.update.mockResolvedValue(undefined); mocks.create.mockResolvedValue({ id: 1 }); });

describe("Library processing options", () => {
  it("renders AI-only explicit intent as effective checked locks with persistent accessible reasons", async () => {
    await openEdit();
    expect(switchFor("Recognize subtitles")).toBeChecked(); expect(switchFor("Recognize subtitles")).toBeDisabled();
    expect(switchFor("Extract subtitles")).toBeChecked(); expect(switchFor("Extract subtitles")).toBeDisabled();
    expect(switchFor("Extract audio tracks")).toBeChecked(); expect(switchFor("Extract audio tracks")).toBeDisabled();
    const recognitionReasonId = switchFor("Recognize subtitles").getAttribute("aria-describedby")!;
    expect(document.getElementById(recognitionReasonId)).toHaveTextContent("Required by AI analysis");
    const subtitleReasonId = switchFor("Extract subtitles").getAttribute("aria-describedby")!;
    const atrackReasonId = switchFor("Extract audio tracks").getAttribute("aria-describedby")!;
    expect(document.getElementById(subtitleReasonId)).toHaveTextContent("Required by subtitle recognition");
    expect(document.getElementById(atrackReasonId)).toHaveTextContent("Required by subtitle recognition");
  });

  it("immediate edit submit preserves AI-only explicit provenance", async () => {
    await openEdit(); fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(mocks.update).toHaveBeenCalledWith(7, expect.objectContaining({
      preview_extract: 0, subtitle_extract: 0, atrack_extract: 0,
      subtitle_recognize: 0, keyframe_extract: 1, ai_analysis: 1,
    }));
  });

  it("enabling AI derives checked locks but submits AI as the only new explicit choice", async () => {
    await openEdit(library({ ai_analysis: 0, keyframe_extract: 0, processing_options: undefined }));
    fireEvent.click(switchFor("AI analysis"));
    expect(switchFor("Recognize subtitles")).toBeChecked(); expect(switchFor("Recognize subtitles")).toBeDisabled();
    expect(switchFor("Extract subtitles")).toBeChecked(); expect(switchFor("Extract subtitles")).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" })); await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(mocks.update).toHaveBeenCalledWith(7, expect.objectContaining({ subtitle_extract: 0, atrack_extract: 0, subtitle_recognize: 0, ai_analysis: 1 }));
  });

  it("promotes dependencies only when disabling their enforcing option", async () => {
    await openEdit();
    fireEvent.click(switchFor("AI analysis"));
    expect(switchFor("Recognize subtitles")).toBeChecked(); expect(switchFor("Recognize subtitles")).not.toBeDisabled();
    fireEvent.click(switchFor("Recognize subtitles"));
    expect(switchFor("Extract subtitles")).toBeChecked(); expect(switchFor("Extract subtitles")).not.toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" })); await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(mocks.update).toHaveBeenCalledWith(7, expect.objectContaining({ ai_analysis: 0, subtitle_recognize: 0, subtitle_extract: 1, atrack_extract: 1 }));
  });

  it("create submits explicit choices rather than effective closure", async () => {
    mocks.fetch.mockResolvedValue(result([])); render(<I18nProvider locale="en"><LibraryPage /></I18nProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "New library" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "New Videos" } });
    fireEvent.change(screen.getByLabelText("Folders (one per line)"), { target: { value: "E:/videos" } });
    fireEvent.click(switchFor("AI analysis")); fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.create).toHaveBeenCalled());
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({ subtitle_extract: 0, atrack_extract: 0, subtitle_recognize: 0, ai_analysis: 1 }));
  });

  it("hides controls for non-video types without clearing explicit values", async () => {
    await openEdit(library({ ai_analysis: 0, subtitle_extract: 1, keyframe_extract: 0, processing_options: undefined }));
    const typeItem = screen.getByText("Type", { selector: "label" }).closest(".ant-form-item")!;
    fireEvent.mouseDown(within(typeItem as HTMLElement).getByRole("combobox")); fireEvent.click(await screen.findByText("Music", { selector: ".ant-select-item-option-content" }));
    expect(screen.queryByText("Extract subtitles")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save" })); await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(mocks.update).toHaveBeenCalledWith(7, expect.objectContaining({ type: "music", subtitle_extract: 1 }));
  });
});
