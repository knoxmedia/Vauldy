import { describe, expect, it, vi } from "vitest";
import { createLibraryRequestScope } from "../sharedLibraryRequest";

function deferred<T>() { let resolve!: (v:T)=>void; let reject!:(e:unknown)=>void; const promise=new Promise<T>((r,j)=>{resolve=r;reject=j}); return {promise,resolve,reject}; }
const tick = async () => { await Promise.resolve(); await Promise.resolve(); };

describe("library request scope", () => {
  it("shares one physical request for concurrent consumers", async () => {
    const pending=deferred<number>(); const load=vi.fn(()=>pending.promise); const scope=createLibraryRequestScope(load);
    const a=scope.load(); const b=scope.load(); expect(load).toHaveBeenCalledTimes(1);
    pending.resolve(7); await expect(Promise.all([a,b])).resolves.toEqual([7,7]);
  });
  it("does not abort the physical request while another consumer remains", async () => {
    const pending=deferred<number>(); let physical!:AbortSignal;
    const scope=createLibraryRequestScope((signal)=>{physical=signal; return pending.promise});
    const ca=new AbortController(); const cb=new AbortController();
    const a=scope.load(ca.signal); const b=scope.load(cb.signal); ca.abort();
    await expect(a).rejects.toMatchObject({name:"AbortError"}); expect(physical.aborted).toBe(false);
    pending.resolve(9); await expect(b).resolves.toBe(9);
  });
  it("aborts when all consumers leave and dispose aborts the generation", async () => {
    const pending=deferred<number>(); let physical!:AbortSignal;
    const scope=createLibraryRequestScope((signal)=>{physical=signal; return pending.promise});
    const c=new AbortController(); const result=scope.load(c.signal); c.abort();
    await expect(result).rejects.toMatchObject({name:"AbortError"}); expect(physical.aborted).toBe(true);
    const second=scope.load(); scope.dispose(); await expect(second).rejects.toMatchObject({name:"AbortError"});
  });

  it("detaches an unobserved aborted request before starting its replacement", async () => {
    const old = deferred<number>();
    const fresh = deferred<number>();
    const loader = vi.fn().mockImplementationOnce(() => old.promise).mockImplementationOnce(() => fresh.promise);
    const scope = createLibraryRequestScope(loader);
    const consumer = new AbortController();
    const abandoned = scope.load(consumer.signal);
    consumer.abort();
    await expect(abandoned).rejects.toMatchObject({ name: "AbortError" });

    const replacement = scope.load();
    expect(loader).toHaveBeenCalledTimes(2);
    old.reject(new Error("late old failure"));
    fresh.resolve(11);
    await expect(replacement).resolves.toBe(11);
  });
  it("never shares across scope instances", async () => {
    const load=vi.fn().mockResolvedValue(1); const a=createLibraryRequestScope(load); const b=createLibraryRequestScope(load);
    await Promise.all([a.load(),b.load()]); expect(load).toHaveBeenCalledTimes(2); await tick();
  });
});
