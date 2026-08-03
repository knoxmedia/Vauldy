import { useCallback, useState } from "react";
import type { AllowedActions, ProjectionRow, BatchResult } from "../../api/taskControl";
import { fetchTaskControlActions } from "../../api/taskControl";

export interface TaskActionsProps {
  taskId: string;
  actions: AllowedActions;
  revision?: number;
  generation?: number;
  retryRound?: number;
  onSuccess?: (row: ProjectionRow) => void;
  onConflict?: (error: string, row?: ProjectionRow) => void;
}

type PendingAction = "abort" | "remove" | "reset" | "run_now" | "skip" | "reopen" | null;

export function TaskActions({
  taskId,
  actions,
  revision,
  generation,
  retryRound,
  onSuccess,
  onConflict,
}: TaskActionsProps) {
  const [pending, setPending] = useState<PendingAction>(null);
  const [reason, setReason] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showReason, setShowReason] = useState(false);

  const handleAction = useCallback(
    async (action: string) => {
      setPending(action as PendingAction);
      setError(null);

      if (needsReason(action)) {
        setShowReason(true);
        return;
      }

      await executeAction(action, "");
      setPending(null);
      setShowReason(false);
      setReason("");
    },
    [taskId, revision, generation, retryRound, onSuccess, onConflict],
  );

  const handleConfirm = useCallback(async () => {
    if (!pending) return;
    if (needsReason(pending) && !reason.trim()) {
      setError("Reason is required");
      return;
    }
    await executeAction(pending, reason);
    setPending(null);
    setShowReason(false);
    setReason("");
    setError(null);
  }, [pending, reason]);

  const handleCancel = useCallback(() => {
    setPending(null);
    setShowReason(false);
    setReason("");
    setError(null);
  }, []);

  const executeAction = useCallback(
    async (action: string, reasonText: string) => {
      setLoading(true);
      try {
        const result = await fetchTaskControlActions(taskId, {
          action,
          reason: reasonText || action,
          expected_revision: revision,
          expected_generation: generation,
          expected_retry_round: retryRound,
        });
        if (result.row) {
          onSuccess?.(result.row);
        }
      } catch (err: unknown) {
        const ax = err as { response?: { status?: number; data?: { error?: string; message?: string; row?: ProjectionRow } } };
        if (ax.response?.status === 409) {
          onConflict?.(
            ax.response.data?.message || "Conflict",
            ax.response.data?.row,
          );
        } else {
          setError(ax.response?.data?.message || "Action failed");
        }
      } finally {
        setLoading(false);
      }
    },
    [taskId, revision, generation, retryRound, onSuccess, onConflict],
  );

  const actionButtons = [
    { key: "abort", label: "Abort", icon: "⏹", show: actions.abort, color: "#faad14" },
    { key: "remove", label: "Remove", icon: "🗑", show: actions.remove, color: "#ff4d4f" },
    { key: "reset", label: "Reset", icon: "↻", show: actions.reset, color: "#1677ff" },
    { key: "run_now", label: "Run Now", icon: "⚡", show: actions.run_now, color: "#52c41a" },
    { key: "skip", label: "Skip", icon: "⏭", show: actions.skip, color: "#888" },
    { key: "reopen", label: "Reopen", icon: "🔓", show: actions.reopen, color: "#1677ff" },
  ].filter((b) => b.show);

  if (actionButtons.length === 0) return null;

  return (
    <div style={{ display: "flex", gap: 6, flexWrap: "wrap", alignItems: "center" }}>
      {pending !== null && showReason ? (
        <div style={{ display: "flex", gap: 8, alignItems: "center", width: "100%" }}>
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={`Reason for ${pending}`}
            style={{
              flex: 1,
              padding: "4px 8px",
              background: "#1a1a1a",
              color: "#d9d9d9",
              border: `1px solid ${error ? "#ff4d4f" : "#303030"}`,
              borderRadius: 4,
              fontSize: 13,
            }}
            aria-label="Reason"
            autoFocus
          />
          <button
            onClick={handleConfirm}
            disabled={loading}
            style={{
              padding: "4px 12px",
              background: "#1677ff",
              color: "#fff",
              border: "none",
              borderRadius: 4,
              cursor: "pointer",
              fontSize: 13,
            }}
          >
            Confirm
          </button>
          <button
            onClick={handleCancel}
            style={{
              padding: "4px 12px",
              background: "#1a1a1a",
              color: "#aaa",
              border: "1px solid #303030",
              borderRadius: 4,
              cursor: "pointer",
              fontSize: 13,
            }}
          >
            Cancel
          </button>
        </div>
      ) : (
        <>
          {actionButtons.map((btn) => (
            <button
              key={btn.key}
              onClick={() => handleAction(btn.key)}
              disabled={loading || pending !== null}
              title={btn.label}
              style={{
                padding: "3px 10px",
                background: pending === btn.key ? `${btn.color}22` : "#1a1a1a",
                color: btn.color,
                border: `1px solid ${pending === btn.key ? btn.color : "#303030"}`,
                borderRadius: 4,
                cursor: "pointer",
                fontSize: 12,
                display: "flex",
                alignItems: "center",
                gap: 4,
                opacity: loading && pending !== btn.key ? 0.5 : 1,
              }}
            >
              <span>{btn.icon}</span>
              <span>{btn.label}</span>
            </button>
          ))}
        </>
      )}

      {error && !showReason && (
        <div style={{ width: "100%", color: "#ff4d4f", fontSize: 12, marginTop: 4 }}>{error}</div>
      )}
    </div>
  );
}

function needsReason(action: string): boolean {
  return ["remove", "reset", "reopen"].includes(action);
}

// Batch action helpers for Task 14
export interface BatchActionState {
  batchItems: string[];
  operationId: string;
  action: string;
  reason: string;
  results: BatchResult | null;
  loading: boolean;
  error: string | null;
}

export function createBatchActionState(): BatchActionState {
  return {
    batchItems: [],
    operationId: crypto.randomUUID(),
    action: "",
    reason: "",
    results: null,
    loading: false,
    error: null,
  };
}
