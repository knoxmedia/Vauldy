import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Library } from "../../api/client";
import { useAuthStore } from "../../store/auth";

const mocks=vi.hoisted(()=>({ fetch:vi.fn(), scan:vi.fn(), error:vi.fn() }));
vi.mock("antd",()=>({
  Button:({children,onClick}:{children?:React.ReactNode;onClick?:()=>void})=><button onClick={onClick}>{children}</button>,
  Col:({children}:{children?:React.ReactNode})=><>{children}</>, Divider:({children}:{children?:React.ReactNode})=><>{children}</>,
  Drawer:({open}:{open:boolean})=>open?<div data-testid="drawer">drawer</div>:null,
  Form:{useForm:()=>[{resetFields:vi.fn(),setFieldsValue:vi.fn(),validateFields:vi.fn()}],Item:({children}:{children?:React.ReactNode})=><>{children}</>},
  Grid:{useBreakpoint:()=>({})}, Input:Object.assign(()=>null,{TextArea:()=>null}), Modal:{confirm:vi.fn()},
  Radio:Object.assign(()=>null,{Group:({children}:{children?:React.ReactNode})=><>{children}</>}), Row:({children}:{children?:React.ReactNode})=><>{children}</>,
  Select:()=>null, Space:({children}:{children?:React.ReactNode})=><>{children}</>, Switch:()=>null,
  Table:({dataSource}:{dataSource:Library[]})=><div data-testid="rows">{dataSource.map(x=>`${x.id}:${x.name}:${x.path}:${x.scan_status}:${x.scan_processed_count}`).join("|")}</div>,
  Tag:({children}:{children?:React.ReactNode})=><>{children}</>, Tooltip:({children}:{children?:React.ReactNode})=><>{children}</>,
  message:{error:mocks.error,success:vi.fn(),warning:vi.fn()},
}));
vi.mock("@ant-design/icons",()=>({QuestionCircleOutlined:()=>null}));
vi.mock("../../components/LibraryProviderSourceTabs",()=>({default:()=>null}));
vi.mock("../../api/client",async(importOriginal)=>{
 const actual=await importOriginal<typeof import("../../api/client")>();
 return {...actual,fetchLibrariesWithCapabilities:mocks.fetch,scanLibrary:mocks.scan,cancelScanTask:vi.fn(),createLibrary:vi.fn(),deleteLibrary:vi.fn(),updateLibrary:vi.fn()};
});

import { LibraryRequestScopeProvider } from "../../lib/libraryRequestScope";
import LibraryPage from "../Library";
const lib=(status:string,processed=0):Library=>({id:1,name:"Movies",type:"movie",path:"",auto_scan:0,scraper:"",created_at:"",scan_status:status,scan_processed_count:processed});
const result=(items:Library[])=>({items,drmCapabilities:{widevine_enabled:true,powerdrm_enabled:true},encryptedAssetsConfig:{}});
const tick=async()=>{await Promise.resolve();await Promise.resolve()};

afterEach(()=>{vi.useRealTimers();Object.defineProperty(document,"hidden",{configurable:true,value:false});});
beforeEach(()=>{vi.clearAllMocks();vi.useFakeTimers();useAuthStore.setState({token:"token-a",username:"alice"});});

describe("Library polling",()=>{
 it("polls at 3s while active then 15s idle without overlap",async()=>{
  let resolveSlow!:(v:ReturnType<typeof result>)=>void;
  mocks.fetch.mockResolvedValueOnce(result([lib("running",1)])).mockImplementationOnce(()=>new Promise(r=>{resolveSlow=r})).mockResolvedValue(result([lib("idle",2)]));
  render(<LibraryPage/>); await tick(); expect(mocks.fetch).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(3000); expect(mocks.fetch).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(30000); expect(mocks.fetch).toHaveBeenCalledTimes(2);
  resolveSlow(result([lib("idle",2)])); await tick();
  await vi.advanceTimersByTimeAsync(14999); expect(mocks.fetch).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(1); expect(mocks.fetch).toHaveBeenCalledTimes(3);
 });

 it("aborts on unmount and refreshes immediately on focus",async()=>{
  let signal!:AbortSignal; mocks.fetch.mockImplementation((_s?:AbortSignal)=>{signal=_s!;return new Promise(()=>undefined)});
  const view=render(<LibraryPage/>); await tick(); window.dispatchEvent(new Event("focus")); expect(mocks.fetch).toHaveBeenCalledTimes(1);
  view.unmount(); expect(signal.aborted).toBe(true);
 });

 it("keys all Library UI state to the auth request scope",async()=>{
  const old={...lib("idle"),name:"Alice Library",path:"C:/alice"};
  mocks.fetch.mockResolvedValueOnce(result([old])).mockImplementationOnce(()=>new Promise(()=>undefined));
  render(<LibraryRequestScopeProvider><LibraryPage/></LibraryRequestScopeProvider>);
  await vi.waitFor(()=>expect(screen.getByTestId("rows")).toHaveTextContent("Alice Library"));
  act(()=>screen.getAllByRole("button")[0]!.click());
  expect(screen.getByTestId("drawer")).toBeInTheDocument();
  act(()=>useAuthStore.setState({token:"token-b",username:"bob"}));
  expect(screen.getByTestId("rows")).not.toHaveTextContent("Alice Library");
  expect(screen.getByTestId("rows")).not.toHaveTextContent("C:/alice");
  expect(screen.queryByTestId("drawer")).not.toBeInTheDocument();
 });

 it("pauses while hidden, aborts in-flight, and resumes one guarded request",async()=>{
  vi.setSystemTime(1_000);
  let hidden=false;
  Object.defineProperty(document,"hidden",{configurable:true,get:()=>hidden});
  let oldSignal!:AbortSignal;
  let resolveOld!:(v:ReturnType<typeof result>)=>void;
  mocks.fetch.mockImplementationOnce((signal?:AbortSignal)=>{oldSignal=signal!;return new Promise(r=>{resolveOld=r})}).mockResolvedValue(result([lib("idle",2)]));
  render(<LibraryPage/>); await vi.waitFor(()=>expect(mocks.fetch).toHaveBeenCalledTimes(1));
  hidden=true; document.dispatchEvent(new Event("visibilitychange"));
  expect(oldSignal.aborted).toBe(true);
  resolveOld(result([{...lib("idle",99),name:"stale"}])); await tick();
  expect(screen.getByTestId("rows")).not.toHaveTextContent("stale");
  await vi.advanceTimersByTimeAsync(60_000); expect(mocks.fetch).toHaveBeenCalledTimes(1);
  hidden=false; document.dispatchEvent(new Event("visibilitychange")); window.dispatchEvent(new Event("focus"));
  await tick(); expect(mocks.fetch).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(250); expect(mocks.fetch).toHaveBeenCalledTimes(2);
 });
});
