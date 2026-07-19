import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { MediaItem } from "../../api/client";
import { I18nProvider } from "../../i18n";
import { useAuthStore } from "../../store/auth";

const mocks = vi.hoisted(() => ({
  fetchMedia: vi.fn(), categories: vi.fn(), places: vi.fn(), persons: vi.fn(),
  classify: vi.fn(), location: vi.fn(), face: vi.fn(),
}));
vi.mock("../../api/client", async (importOriginal) => ({ ...(await importOriginal<typeof import("../../api/client")>()),
  fetchMedia: mocks.fetchMedia, fetchPhotoCategories: mocks.categories, fetchPhotoPlaces: mocks.places,
  fetchPhotoPersons: mocks.persons, fetchPhotoClassifyProgress: mocks.classify,
  fetchPhotoLocationProgress: mocks.location, fetchPhotoFaceProgress: mocks.face,
}));
vi.mock("../../components/PhotoListView", () => ({ default: ({ items }: { items: MediaItem[] }) => <>{items.map((i) => <div key={i.id}>{i.title}</div>)}</> }));
vi.mock("../../components/PhotoSmartClassify", () => ({ default: ({categories,places,persons}: any) => <div data-testid="smart-meta">{[...categories,...places,...persons].map((x:any)=>x.name).join(",")}</div>, PhotoPersonAllGrid: () => null, PhotoPlaceAllGrid: () => null }));
vi.mock("../../components/PhotoTimelineRail", () => ({ default: () => null }));
vi.mock("../../components/PhotoLightbox", () => ({ default: () => null }));
vi.mock("../../components/PhotoPersonDrillTitle", () => ({ default: () => null }));
vi.mock("antd", () => ({ Button: (p: any) => <button onClick={p.onClick}>{p.children}</button>, Empty: () => <div>empty</div>, Input: { Search: () => null }, Progress: () => null, Select: () => null, Space: ({children}: any) => <>{children}</>, Spin: () => <div role="status">rows loading</div>, Alert: ({message}: any) => <div role="alert">{message}</div>, Tabs: ({onChange}: any) => <button onClick={()=>onChange("smart")}>smart</button>, Tooltip: ({children}: any) => <>{children}</>, message: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn() } }));
vi.mock("@ant-design/icons", () => ({ ArrowLeftOutlined:()=>null,EnvironmentOutlined:()=>null,PictureOutlined:()=>null,SyncOutlined:()=>null,TagOutlined:()=>null,UserOutlined:()=>null }));

import PhotoBrowse from "../PhotoBrowse";
const row = { id: 10, title: "Visible photo", file_type: "image" } as MediaItem;
const zero = { percent: 100, pending: 0 };
function deferred<T>() { let resolve!: (value:T)=>void; const promise=new Promise<T>((r)=>{resolve=r}); return {promise,resolve}; }
function renderPhoto(signal?: AbortSignal) { return render(<I18nProvider locale="en"><PhotoBrowse libraryId={1} signal={signal}/></I18nProvider>); }

beforeEach(() => { vi.clearAllMocks(); vi.useRealTimers(); useAuthStore.setState({ role: "admin" }); mocks.categories.mockResolvedValue([]); mocks.places.mockResolvedValue([]); mocks.persons.mockResolvedValue([]); mocks.classify.mockResolvedValue(zero); mocks.location.mockResolvedValue(zero); mocks.face.mockResolvedValue(zero); });

describe("PhotoBrowse request state machine", () => {
  it("renders rows while smart metadata remains deferred", async () => {
    const meta = deferred<never[]>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.categories.mockReturnValue(meta.promise); mocks.places.mockReturnValue(meta.promise); mocks.persons.mockReturnValue(meta.promise);
    renderPhoto(); await waitFor(() => expect(screen.getByText("Visible photo")).toBeInTheDocument()); expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
  it("polls only while pending, never overlaps, and completion refreshes metadata without fetching rows", async () => {
    vi.useFakeTimers(); const slow = deferred<typeof zero>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.classify.mockResolvedValueOnce({percent:10,pending:1}).mockReturnValueOnce(slow.promise); mocks.location.mockResolvedValue(zero); mocks.face.mockResolvedValue(zero);
    renderPhoto(); await vi.waitFor(() => expect(mocks.classify).toHaveBeenCalledTimes(1)); await act(async () => {}); await vi.advanceTimersByTimeAsync(8000); expect(mocks.classify).toHaveBeenCalledTimes(2); await vi.advanceTimersByTimeAsync(16000); expect(mocks.classify).toHaveBeenCalledTimes(2);
    slow.resolve(zero); await act(async()=>{}); await vi.waitFor(() => expect(mocks.categories).toHaveBeenCalledTimes(2)); expect(mocks.fetchMedia).toHaveBeenCalledTimes(1); await vi.advanceTimersByTimeAsync(16000); expect(mocks.classify).toHaveBeenCalledTimes(2);
  });
  it("aborts all child requests when the route signal aborts", async () => {
    const parent = new AbortController(); mocks.fetchMedia.mockReturnValue(new Promise(()=>undefined)); mocks.categories.mockReturnValue(new Promise(()=>undefined)); renderPhoto(parent.signal); await waitFor(() => expect(mocks.fetchMedia).toHaveBeenCalled()); const rowSignal=mocks.fetchMedia.mock.calls[0]![2] as AbortSignal; const metaSignal=mocks.categories.mock.calls[0]![1] as AbortSignal; parent.abort(); expect(rowSignal.aborted).toBe(true); expect(metaSignal.aborted).toBe(true);
  });

  it("commits successful metadata independently and renders nonblocking errors", async () => {
    mocks.fetchMedia.mockResolvedValue([row]); mocks.categories.mockRejectedValue(new Error("categories down")); mocks.places.mockResolvedValue([{id:"p",name:"Place",count:1}]); mocks.persons.mockResolvedValue([]);
    renderPhoto(); await waitFor(() => expect(screen.getByText("Visible photo")).toBeInTheDocument());
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument()); expect(mocks.places).toHaveBeenCalledTimes(1);
  });
  it.each([
    ["classify", mocks.classify, mocks.categories],
    ["location", mocks.location, mocks.places],
    ["face", mocks.face, mocks.persons],
  ])("refreshes only %s metadata when that task completes", async (_kind, progressMock, metaMock) => {
    vi.useFakeTimers(); mocks.fetchMedia.mockResolvedValue([row]); progressMock.mockResolvedValueOnce({percent:10,pending:1}).mockResolvedValueOnce(zero);
    renderPhoto(); await vi.waitFor(() => expect(progressMock).toHaveBeenCalledTimes(1)); await act(async()=>{}); await vi.advanceTimersByTimeAsync(8000);
    await vi.waitFor(() => expect(metaMock).toHaveBeenCalledTimes(2));
    expect([mocks.categories,mocks.places,mocks.persons].filter((m)=>m!==metaMock).map((m)=>m.mock.calls.length)).toEqual([1,1]); expect(mocks.fetchMedia).toHaveBeenCalledTimes(1);
  });
  it("starts new generation progress when library changes with old pending", async () => {
    const old=deferred<typeof zero>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.classify.mockReturnValueOnce(old.promise).mockResolvedValue(zero);
    const view=render(<I18nProvider locale="en"><PhotoBrowse libraryId={1}/></I18nProvider>); await waitFor(()=>expect(mocks.classify).toHaveBeenCalledTimes(1));
    view.rerender(<I18nProvider locale="en"><PhotoBrowse libraryId={2}/></I18nProvider>); await waitFor(()=>expect(mocks.classify).toHaveBeenCalledTimes(2));
  });


  it("different metadata kinds commit without invalidating a deferred kind", async () => {
    vi.useFakeTimers(); const categories=deferred<any[]>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.categories.mockReturnValueOnce(categories.promise); mocks.places.mockResolvedValueOnce([{id:"initial",name:"Initial place",count:1}]).mockResolvedValueOnce([{id:"fresh",name:"Fresh place",count:1}]); mocks.location.mockResolvedValueOnce({percent:10,pending:1}).mockResolvedValueOnce(zero);
    renderPhoto(); fireEvent.click(screen.getByText("smart")); await vi.waitFor(()=>expect(mocks.location).toHaveBeenCalledTimes(1)); await act(async()=>{}); await vi.advanceTimersByTimeAsync(8000);
    await vi.waitFor(()=>expect(mocks.places).toHaveBeenCalledTimes(2));
    categories.resolve([{id:"cat",name:"Late category",count:1}]); await act(async()=>{});
    await vi.waitFor(()=>expect(screen.getByTestId("smart-meta")).toHaveTextContent("Late category,Fresh place"));
  });
  it("coalesces an overlapping kind and performs one dirty rerun", async () => {
    vi.useFakeTimers(); const first=deferred<any[]>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.categories.mockReturnValueOnce(first.promise).mockResolvedValueOnce([{id:"fresh",name:"Fresh category",count:1}]); mocks.classify.mockResolvedValueOnce({percent:10,pending:1}).mockResolvedValueOnce(zero);
    renderPhoto(); fireEvent.click(screen.getByText("smart")); await vi.waitFor(()=>expect(mocks.categories).toHaveBeenCalledTimes(1)); await act(async()=>{}); await vi.advanceTimersByTimeAsync(8000);
    expect(mocks.categories).toHaveBeenCalledTimes(1); first.resolve([{id:"old",name:"Old category",count:1}]); await act(async()=>{});
    await vi.waitFor(()=>expect(mocks.categories).toHaveBeenCalledTimes(2)); await vi.waitFor(()=>expect(screen.getByTestId("smart-meta")).toHaveTextContent("Fresh category"));
  });
  it("does not commit stale metadata after a route switch", async () => {
    const stale=deferred<any[]>(); mocks.fetchMedia.mockResolvedValue([row]); mocks.categories.mockReturnValueOnce(stale.promise).mockResolvedValueOnce([{id:"new",name:"New category",count:1}]);
    const view=render(<I18nProvider locale="en"><PhotoBrowse libraryId={1}/></I18nProvider>); fireEvent.click(screen.getByText("smart")); await waitFor(()=>expect(mocks.categories).toHaveBeenCalledTimes(1));
    view.rerender(<I18nProvider locale="en"><PhotoBrowse libraryId={2}/></I18nProvider>); fireEvent.click(screen.getByText("smart")); await waitFor(()=>expect(screen.getByTestId("smart-meta")).toHaveTextContent("New category"));
    stale.resolve([{id:"old",name:"Stale category",count:1}]); await act(async()=>{}); expect(screen.getByTestId("smart-meta")).not.toHaveTextContent("Stale category");
  });


  it("clears old progress and stops polling when new library progress fails", async () => {
    vi.useFakeTimers(); mocks.fetchMedia.mockResolvedValue([row]); mocks.classify.mockResolvedValueOnce({percent:10,pending:1});
    const view=render(<I18nProvider locale="en"><PhotoBrowse libraryId={1}/></I18nProvider>); await vi.waitFor(()=>expect(screen.getByText("AI classification in progress")).toBeInTheDocument());
    mocks.classify.mockRejectedValueOnce(new Error("new failed")); mocks.location.mockRejectedValueOnce(new Error("new failed")); mocks.face.mockRejectedValueOnce(new Error("new failed"));
    view.rerender(<I18nProvider locale="en"><PhotoBrowse libraryId={2}/></I18nProvider>); await vi.waitFor(()=>expect(screen.queryByText("AI classification in progress")).not.toBeInTheDocument());
    const calls=mocks.classify.mock.calls.length; await vi.advanceTimersByTimeAsync(24000); expect(mocks.classify).toHaveBeenCalledTimes(calls);
  });


  it("clears committed smart metadata immediately when library generation changes", async () => {
    mocks.fetchMedia.mockResolvedValue([row]);
    mocks.categories.mockResolvedValueOnce([{id:"old-cat",name:"Old category",count:1}]);
    mocks.places.mockResolvedValueOnce([{id:"old-place",name:"Old place",count:1}]);
    mocks.persons.mockResolvedValueOnce([{id:9,name:"Old person",count:1}]);
    const view=render(<I18nProvider locale="en"><PhotoBrowse libraryId={1}/></I18nProvider>);
    fireEvent.click(screen.getByText("smart"));
    await waitFor(()=>expect(screen.getByTestId("smart-meta")).toHaveTextContent("Old category,Old place,Old person"));
    const next=deferred<any[]>(); mocks.categories.mockReturnValueOnce(next.promise); mocks.places.mockReturnValueOnce(next.promise); mocks.persons.mockReturnValueOnce(next.promise);
    view.rerender(<I18nProvider locale="en"><PhotoBrowse libraryId={2}/></I18nProvider>);
    await waitFor(()=>expect(screen.getByText("Visible photo")).toBeInTheDocument());
    fireEvent.click(screen.getByText("smart"));
    expect(screen.getByTestId("smart-meta")).not.toHaveTextContent("Old category");
    expect(screen.getByTestId("smart-meta")).not.toHaveTextContent("Old place");
    expect(screen.getByTestId("smart-meta")).not.toHaveTextContent("Old person");
  });

});