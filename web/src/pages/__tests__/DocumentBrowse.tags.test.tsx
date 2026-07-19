import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { DocumentItem } from "../../api/client";

const mocks = vi.hoisted(() => ({
  fetchDocuments: vi.fn(), fetchDocumentNodes: vi.fn(), fetchDocumentFacets: vi.fn(), fetchRecentDocuments: vi.fn(),
  batchUpdate: vi.fn(), messageError: vi.fn(), t: (key: string) => key, modal: undefined as undefined | { onOk?: () => Promise<void> },
}));
vi.mock("../../api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/client")>()),
  fetchDocuments: mocks.fetchDocuments, fetchDocumentNodes: mocks.fetchDocumentNodes, fetchDocumentFacets: mocks.fetchDocumentFacets,
  fetchRecentDocuments: mocks.fetchRecentDocuments, batchUpdateDocumentTags: mocks.batchUpdate,
  documentCoverSrc: (id: number) => String(id), batchDownloadDocuments: vi.fn(), deleteMedia: vi.fn(),
}));
vi.mock("../../i18n", () => ({ useT: () => mocks.t }));
vi.mock("antd", () => ({
  Breadcrumb: () => null, Tabs: () => null, Tree: () => null, Spin: () => null, Empty: ({description}:{description?:unknown}) => <div>{String(description||"")}</div>, Space: ({children}:{children?:React.ReactNode}) => <>{children}</>,
  Button: ({children,onClick}:{children?:React.ReactNode;onClick?:()=>void}) => <button onClick={onClick}>{children}</button>,
  Checkbox: ({checked,onChange,children}:{checked?:boolean;onChange?:()=>void;children?:React.ReactNode}) => <label><input type="checkbox" checked={checked} onChange={onChange}/>{children}</label>,
  Input: Object.assign(({value,onChange,placeholder,"aria-label":ariaLabel}:{value?:string;onChange?:(e:React.ChangeEvent<HTMLInputElement>)=>void;placeholder?:string; "aria-label"?:string}) => <input aria-label={ariaLabel ?? placeholder} value={value} onChange={onChange}/>, { Search: ({value,onChange,onSearch}:{value?:string;onChange?:(e:React.ChangeEvent<HTMLInputElement>)=>void;onSearch?:()=>void}) => <><input aria-label="search" value={value} onChange={onChange}/><button onClick={onSearch}>search-submit</button></> }),
  Select: ({value,onChange,options}:{value?:string;onChange?:(v:string)=>void;options?:Array<{value:string;label:string}>}) => <select aria-label="tag-mode" value={value} onChange={e=>onChange?.(e.target.value)}>{options?.map(o=><option key={o.value} value={o.value}>{o.label}</option>)}</select>,
  Modal: Object.assign(({open,children,onOk}:{open?:boolean;children?:React.ReactNode;onOk?:()=>Promise<void>}) => open ? <div role="dialog">{children}<button onClick={()=>void onOk?.()}>modal-ok</button></div> : null, { confirm: vi.fn() }),
  message: { success: vi.fn(), error: mocks.messageError, warning: vi.fn() },
}));
vi.mock("@ant-design/icons", () => ({ AppstoreOutlined:()=>null, DeleteOutlined:()=>null, DownloadOutlined:()=>null, FolderOutlined:()=>null, ReadOutlined:()=>null, UnorderedListOutlined:()=>null, TagsOutlined:()=>null }));

import DocumentBrowse from "../DocumentBrowse";
const docs: DocumentItem[] = [{id:1,title:"One",format:"pdf",tags:["Alpha"]},{id:2,title:"Two",format:"pdf",tags:["Beta"]}];
function deferred<T>() { let resolve!:(v:T)=>void,reject!:(e:unknown)=>void; const promise=new Promise<T>((r,j)=>{resolve=r;reject=j});return{promise,resolve,reject}; }
async function setup(){ render(<MemoryRouter><DocumentBrowse libraryId={1}/></MemoryRouter>); await waitFor(()=>expect(screen.getByText("One")).toBeInTheDocument()); fireEvent.click(screen.getAllByRole("checkbox")[1]!); fireEvent.click(screen.getByText("pages.document_browse.batch_tags")); fireEvent.change(screen.getByLabelText("pages.document_browse.batch_tags_input"),{target:{value:"Gamma"}}); }

describe("DocumentBrowse batch tags",()=>{
 beforeEach(()=>{mocks.fetchDocuments.mockResolvedValue(docs);mocks.fetchDocumentNodes.mockResolvedValue([]);mocks.fetchDocumentFacets.mockResolvedValue([{name:"Alpha",count:1},{name:"Beta",count:1}]);mocks.fetchRecentDocuments.mockResolvedValue([]);mocks.batchUpdate.mockReset();});
 it("uses one PATCH and updates selected row and tag facets locally",async()=>{mocks.batchUpdate.mockResolvedValue({updated:1,items:[{media_id:1,tags:["Alpha","Gamma"]}],facet_deltas:[{tag:"gamma",delta:2}]});await setup();const listCalls=mocks.fetchDocuments.mock.calls.length;fireEvent.click(screen.getByText("modal-ok"));await waitFor(()=>expect(mocks.batchUpdate).toHaveBeenCalledTimes(1));expect(mocks.fetchDocuments).toHaveBeenCalledTimes(listCalls);expect(screen.getByText(/Gamma/)).toBeInTheDocument();});
 it("keeps old row state when the PATCH fails",async()=>{const d=deferred<never>();mocks.batchUpdate.mockReturnValue(d.promise);await setup();fireEvent.click(screen.getByText("modal-ok"));await act(async()=>d.reject(new Error("fail")));expect(screen.getByText("Alpha")).toBeInTheDocument();expect(screen.queryByText("Gamma")).not.toBeInTheDocument();});
});








describe("DocumentBrowse request generations", () => {
  beforeEach(() => {
    mocks.fetchDocuments.mockReset();
    mocks.fetchDocumentFacets.mockReset();
    mocks.fetchDocumentNodes.mockResolvedValue([]);
    mocks.fetchRecentDocuments.mockResolvedValue([]);
    mocks.batchUpdate.mockReset();
  });

  it("keeps mutation rows and facets when older requests resolve later", async () => {
    const oldItems = deferred<DocumentItem[]>();
    const oldFacets = deferred<Array<{ name: string; count: number }>>();
    mocks.fetchDocuments.mockReturnValue(oldItems.promise);
    mocks.fetchDocumentFacets.mockReturnValue(oldFacets.promise);
    mocks.batchUpdate.mockResolvedValue({ updated: 1, items: [{ media_id: 1, tags: ["Gamma"] }], facet_deltas: [{ tag: "alpha", delta: -2 }, { tag: "gamma", delta: 2 }] });
    render(<MemoryRouter><DocumentBrowse libraryId={1}/></MemoryRouter>);
    await act(async () => oldItems.resolve(docs));
    await waitFor(() => expect(screen.getByText("One")).toBeInTheDocument());
    fireEvent.click(screen.getAllByRole("checkbox")[1]!);
    fireEvent.click(screen.getByText("pages.document_browse.batch_tags"));
    fireEvent.change(screen.getByLabelText("pages.document_browse.batch_tags_input"), { target: { value: "Gamma" } });
    fireEvent.click(screen.getByText("modal-ok"));
    await waitFor(() => expect(screen.getByText(/Gamma/)).toBeInTheDocument());
    await act(async () => oldFacets.resolve([{ name: "Alpha", count: 9 }]));
    expect(screen.getByText(/Gamma/)).toBeInTheDocument();
  });

  it("drops an older item search response after a newer request", async () => {
    const oldRequest = deferred<DocumentItem[]>();
    const newRequest = deferred<DocumentItem[]>();
    mocks.fetchDocuments.mockImplementation((_libraryId: number, params?: Record<string, unknown>) => params?.q === "new" ? newRequest.promise : oldRequest.promise);
    mocks.fetchDocumentFacets.mockResolvedValue([]);
    render(<MemoryRouter><DocumentBrowse libraryId={1}/></MemoryRouter>);
    const search = screen.getByLabelText("search");
    fireEvent.change(search, { target: { value: "new" } });
    await waitFor(() => expect(mocks.fetchDocuments.mock.calls.some((call) => call[1]?.q === "new")).toBe(true));
    await act(async () => newRequest.resolve([{ id: 3, title: "New", format: "pdf", tags: [] }]));
    expect(await screen.findByText("New")).toBeInTheDocument();
    await act(async () => oldRequest.resolve([{ id: 4, title: "Old", format: "pdf", tags: [] }]));
    expect(screen.queryByText("Old")).not.toBeInTheDocument();
  });
});


describe("DocumentBrowse folder tree lifecycle", () => {
  it("does not report an intentionally aborted tree request during cleanup", async () => {
    mocks.fetchDocuments.mockResolvedValue(docs);
    mocks.fetchDocumentFacets.mockResolvedValue([]);
    mocks.fetchRecentDocuments.mockResolvedValue([]);
    mocks.messageError.mockReset();
    mocks.fetchDocumentNodes.mockImplementation((_libraryId: number, _parent: string, signal: AbortSignal) => new Promise((_, reject) => {
      signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
    }));

    const view = render(<MemoryRouter><DocumentBrowse libraryId={1}/></MemoryRouter>);
    await waitFor(() => expect(mocks.fetchDocumentNodes).toHaveBeenCalled());
    view.unmount();
    await act(async () => undefined);

    expect(mocks.messageError).not.toHaveBeenCalledWith("pages.document_browse.load_tree_failed");
  });
});

describe("DocumentBrowse library session lifecycle", () => {
  it("aborts a pending mutation, clears selection, and uses only new-library ids", async () => {
    const pending = deferred<never>();
    let mutationSignal: AbortSignal | undefined;
    mocks.fetchDocuments.mockImplementation((libraryId: number) => Promise.resolve(libraryId === 1 ? docs : [{ id: 9, title: "Nine", format: "pdf", tags: [] }]));
    mocks.fetchDocumentFacets.mockResolvedValue([]);
    mocks.fetchDocumentNodes.mockResolvedValue([]);
    mocks.fetchRecentDocuments.mockResolvedValue([]);
    mocks.batchUpdate.mockImplementationOnce((_ids, _mode, _tags, signal) => { mutationSignal = signal; return pending.promise; }).mockResolvedValueOnce({ updated: 1, items: [{ media_id: 9, tags: ["New"] }], facet_deltas: [{ tag: "new", delta: 1 }] });
    const view = render(<MemoryRouter><DocumentBrowse libraryId={1}/></MemoryRouter>);
    await waitFor(() => expect(screen.getByText("One")).toBeInTheDocument());
    fireEvent.click(screen.getAllByRole("checkbox")[1]!);
    fireEvent.click(screen.getByText("pages.document_browse.batch_tags"));
    fireEvent.change(screen.getByLabelText("pages.document_browse.batch_tags_input"), { target: { value: "Old" } });
    fireEvent.click(screen.getByText("modal-ok"));
    await waitFor(() => expect(mocks.batchUpdate).toHaveBeenCalledTimes(1));
    view.rerender(<MemoryRouter><DocumentBrowse libraryId={2}/></MemoryRouter>);
    await waitFor(() => expect(mutationSignal?.aborted).toBe(true));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("Nine")).toBeInTheDocument());
    expect(screen.queryByText("pages.document_browse.batch_tags")).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("checkbox")[1]!);
    fireEvent.click(screen.getByText("pages.document_browse.batch_tags"));
    fireEvent.change(screen.getByLabelText("pages.document_browse.batch_tags_input"), { target: { value: "New" } });
    fireEvent.click(screen.getByText("modal-ok"));
    await waitFor(() => expect(mocks.batchUpdate).toHaveBeenCalledTimes(2));
    expect(mocks.batchUpdate.mock.calls[1]?.[0]).toEqual([9]);
  });
});
