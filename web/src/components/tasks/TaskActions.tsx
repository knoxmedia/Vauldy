import { useCallback, useState } from "react";
import { Button, Space, Input, message, Popconfirm } from "antd";
import {
  StopOutlined,
  DeleteOutlined,
  ReloadOutlined,
  RiseOutlined,
  ForwardOutlined,
  UnlockOutlined,
} from "@ant-design/icons";
import type { AllowedActions, ProjectionRow, BatchResult } from "../../api/taskControl";
import { fetchTaskControlActions } from "../../api/taskControl";
import { useT, tGlobal } from "../../i18n";

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

const ACTION_ICONS: Record<string, React.ReactNode> = {
  abort: <StopOutlined />,
  remove: <DeleteOutlined />,
  reset: <ReloadOutlined />,
  run_now: <RiseOutlined />,
  skip: <ForwardOutlined />,
  reopen: <UnlockOutlined />,
};

export function TaskActions({
  taskId,
  actions,
  revision,
  generation,
  retryRound,
  onSuccess,
  onConflict,
}: TaskActionsProps) {
  const t = useT();
  const [pending, setPending] = useState<PendingAction>(null);
  const [reason, setReason] = useState("");
  const [loading, setLoading] = useState(false);
  const [showReason, setShowReason] = useState(false);

  const handleAction = useCallback(
    async (action: string) => {
      setPending(action as PendingAction);

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
      message.warning(tGlobal("tasks.control.reason_required"));
      return;
    }
    await executeAction(pending, reason);
    setPending(null);
    setShowReason(false);
    setReason("");
  }, [pending, reason]);

  const handleCancel = useCallback(() => {
    setPending(null);
    setShowReason(false);
    setReason("");
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
            ax.response.data?.message || tGlobal("tasks.control.conflict"),
            ax.response.data?.row,
          );
        } else {
          message.error(ax.response?.data?.message || tGlobal("tasks.control.action_failed", { action }));
        }
      } finally {
        setLoading(false);
      }
    },
    [taskId, revision, generation, retryRound, onSuccess, onConflict],
  );

  const actionDefs = [
    { key: "abort", label: t("tasks.control.action_abort"), show: actions.abort },
    { key: "remove", label: t("tasks.control.action_remove"), show: actions.remove },
    { key: "reset", label: t("tasks.control.action_reset"), show: actions.reset },
    { key: "run_now", label: t("tasks.control.action_run_now"), show: actions.run_now },
    { key: "skip", label: t("tasks.control.action_skip"), show: actions.skip },
    { key: "reopen", label: t("tasks.control.action_reopen"), show: actions.reopen },
  ].filter((b) => b.show);

  const CONFIRM_KEYS: Record<string, string> = {
    abort: "confirm_abort",
    remove: "confirm_remove",
    reset: "confirm_reset",
    run_now: "confirm_run_now",
    skip: "confirm_skip",
    reopen: "confirm_reopen",
  };

  if (actionDefs.length === 0) return null;

  return (
    <Space size={4} wrap>
      {pending !== null && showReason ? (
        <Space size={8}>
          <Input
            size="small"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("tasks.control.reason_placeholder", { action: t(`tasks.control.action_${pending}`) })}
            style={{ width: 200 }}
            autoFocus
          />
          <Button size="small" type="primary" onClick={handleConfirm} loading={loading}>
            {t("tasks.control.confirm")}
          </Button>
          <Button size="small" onClick={handleCancel}>
            {t("tasks.control.cancel")}
          </Button>
        </Space>
      ) : (
        <>
          {actionDefs.map((btn) => {
            const buttonNode = (
              <Button
                key={btn.key}
                size="small"
                icon={ACTION_ICONS[btn.key]}
                type={pending === btn.key ? "primary" : "default"}
                disabled={loading || pending !== null}
                title={btn.label}
              >
                {btn.label}
              </Button>
            );

            if (needsReason(btn.key)) {
              return (
                <span key={btn.key} onClick={() => handleAction(btn.key)}>
                  {buttonNode}
                </span>
              );
            }

            return (
              <Popconfirm
                key={btn.key}
                title={t(`tasks.control.${CONFIRM_KEYS[btn.key]}`)}
                onConfirm={() => { setPending(btn.key as PendingAction); executeAction(btn.key, btn.key); }}
                okText={t("tasks.control.confirm")}
                cancelText={t("tasks.control.cancel")}
              >
                {buttonNode}
              </Popconfirm>
            );
          })}
        </>
      )}
    </Space>
  );
}

function needsReason(action: string): boolean {
  return ["remove", "reset", "reopen"].includes(action);
}

// Batch action helpers
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
