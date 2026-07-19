export function linkAbortSignal(parent?: AbortSignal): { controller: AbortController; cleanup: () => void } {
  const controller = new AbortController();
  if (!parent) return { controller, cleanup: () => undefined };
  if (parent.aborted) {
    controller.abort(parent.reason);
    return { controller, cleanup: () => undefined };
  }
  const abort = () => controller.abort(parent.reason);
  parent.addEventListener("abort", abort, { once: true });
  return { controller, cleanup: () => parent.removeEventListener("abort", abort) };
}
