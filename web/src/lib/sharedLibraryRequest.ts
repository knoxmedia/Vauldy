export type LibraryRequestScope<T> = {
  load(signal?: AbortSignal): Promise<T>;
  acquireOwner(): () => void;
  dispose(): void;
};

type Consumer<T> = {
  resolve(value: T): void;
  reject(reason: unknown): void;
  signal?: AbortSignal;
  onAbort?: () => void;
  settled: boolean;
};
type Request<T> = { controller: AbortController; consumers: Set<Consumer<T>> };

function abortError(): DOMException {
  return new DOMException("The operation was aborted", "AbortError");
}

export function createLibraryRequestScope<T>(loader: (signal: AbortSignal) => Promise<T>): LibraryRequestScope<T> {
  let disposed = false;
  let owners = 0;
  let ownerVersion = 0;
  let hasOwner = false;
  let inFlight: Request<T> | undefined;

  const detachAndAbort = (request: Request<T>) => {
    if (inFlight === request) inFlight = undefined;
    request.controller.abort();
  };

  const removeConsumer = (request: Request<T>, consumer: Consumer<T>) => {
    request.consumers.delete(consumer);
    if (consumer.signal && consumer.onAbort) consumer.signal.removeEventListener("abort", consumer.onAbort);
    if (request.consumers.size === 0 && !hasOwner && inFlight === request) detachAndAbort(request);
  };

  const settle = (request: Request<T>, kind: "resolve" | "reject", value: unknown) => {
    if (inFlight === request) inFlight = undefined;
    for (const consumer of [...request.consumers]) {
      if (consumer.settled) continue;
      consumer.settled = true;
      removeConsumer(request, consumer);
      if (kind === "resolve") consumer.resolve(value as T);
      else consumer.reject(value);
    }
  };

  const ensureRequest = () => {
    if (inFlight) return inFlight;
    const request: Request<T> = { controller: new AbortController(), consumers: new Set() };
    inFlight = request;
    void loader(request.controller.signal).then(
      (value) => settle(request, "resolve", value),
      (error) => settle(request, "reject", request.controller.signal.aborted ? abortError() : error),
    );
    return request;
  };

  return {
    load(signal) {
      if (disposed || signal?.aborted) return Promise.reject(abortError());
      const request = ensureRequest();
      return new Promise<T>((resolve, reject) => {
        const consumer: Consumer<T> = { resolve, reject, signal, settled: false };
        consumer.onAbort = () => {
          if (consumer.settled) return;
          consumer.settled = true;
          removeConsumer(request, consumer);
          reject(abortError());
        };
        request.consumers.add(consumer);
        signal?.addEventListener("abort", consumer.onAbort, { once: true });
      });
    },
    acquireOwner() {
      if (disposed) return () => undefined;
      owners += 1;
      hasOwner = true;
      ++ownerVersion;
      let released = false;
      return () => {
        if (released) return;
        released = true;
        owners = Math.max(0, owners - 1);
        const releaseVersion = ++ownerVersion;
        queueMicrotask(() => {
          if (disposed || owners !== 0 || ownerVersion !== releaseVersion) return;
          disposed = true;
          const request = inFlight;
          if (!request) return;
          detachAndAbort(request);
          settle(request, "reject", abortError());
        });
      };
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      ++ownerVersion;
      const request = inFlight;
      if (!request) return;
      detachAndAbort(request);
      settle(request, "reject", abortError());
    },
  };
}
