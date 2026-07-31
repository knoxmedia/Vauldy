/** Map domain/raw task status to shared AdminConsole/TaskManager display vocabulary. */
export function toDisplayStatus(type: string, status: string): string {
  if (type === "subtitle" && status === "pending") return "waiting";
  if (type === "preview" && status === "ready") return "done";
  return status;
}

export function matchesDisplayStatus(type: string, rawStatus: string, filter: string): boolean {
  if (filter === "all") return true;
  return toDisplayStatus(type, rawStatus) === filter;
}
