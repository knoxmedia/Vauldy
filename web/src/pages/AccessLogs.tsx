import { Button, Card, DatePicker, Select, Space, Switch, Table, Tag } from "antd";
import { useEffect, useState } from "react";
import { fetchAccessLogs, type AccessLogItem } from "../api/client";
import { renderServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";
import { type Dayjs } from "dayjs";

export default function AccessLogsPage() {
  const t = useT();
  const [rows, setRows] = useState<AccessLogItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [action, setAction] = useState("all");
  const [playOnly, setPlayOnly] = useState(false);
  const [rangePreset, setRangePreset] = useState<"today" | "7d" | "30d" | "custom">("7d");
  const [rangeValue, setRangeValue] = useState<[Dayjs | null, Dayjs | null] | null>(null);

  const parseDevice = (message: string) => {
    const ua = (message.match(/ua=(.*)$/)?.[1] || "").toLowerCase();
    const browser =
      ua.includes("edg/") ? "Edge" : ua.includes("chrome/") ? "Chrome" : ua.includes("firefox/") ? "Firefox" : ua.includes("safari/") ? "Safari" : "-";
    const os =
      ua.includes("windows") ? "Windows" : ua.includes("android") ? "Android" : ua.includes("iphone") || ua.includes("ios") ? "iOS" : ua.includes("mac os") ? "macOS" : ua.includes("linux") ? "Linux" : "-";
    return { browser, os, ua: ua || "-" };
  };
  const parsePlayback = (message: string) => {
    const pos = Number(message.match(/pos=(\d+)/)?.[1] || "0");
    const completed = Number(message.match(/completed=(\d+)/)?.[1] || "0");
    return { pos: Number.isFinite(pos) ? pos : 0, completed: completed > 0 ? 1 : 0 };
  };

  const load = async (selectedAction = action, selectedRange = rangePreset) => {
    setLoading(true);
    try {
      const from = selectedRange === "custom" && rangeValue?.[0] ? rangeValue[0].format("YYYY-MM-DD HH:mm:ss") : undefined;
      const to = selectedRange === "custom" && rangeValue?.[1] ? rangeValue[1].format("YYYY-MM-DD HH:mm:ss") : undefined;
      const actualAction = playOnly ? "playback_all" : selectedAction;
      const raw = await fetchAccessLogs({ limit: 200, action: actualAction, range: selectedRange, from, to });
      setRows(
        playOnly
          ? raw.filter((x) => x.action === "playback_start" || x.action === "playback_end")
          : raw
      );
    } catch {
      setRows([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load("all", "7d");
     
  }, []);

  return (
    <Card
      title={t("pages.access_logs.title")}
      extra={
        <Space>
          <Select
            value={action}
            style={{ width: 150 }}
            onChange={(v) => {
              setAction(v);
              void load(v, rangePreset);
            }}
            options={[
              { value: "all", label: t("pages.access_logs.filter_all_events") },
              { value: "login", label: t("pages.access_logs.filter_login") },
              { value: "logout", label: t("pages.access_logs.filter_logout") },
              { value: "playback_start", label: t("pages.access_logs.filter_playback_start") },
              { value: "playback_end", label: t("pages.access_logs.filter_playback_end") },
            ]}
          />
          <Space size={4}>
            <span style={{ color: "#999" }}>{t("pages.access_logs.only_playback")}</span>
            <Switch
              size="small"
              checked={playOnly}
              onChange={(v) => {
                setPlayOnly(v);
                void load(action, rangePreset);
              }}
            />
          </Space>
          <Select
            value={rangePreset}
            style={{ width: 140 }}
            onChange={(v) => {
              setRangePreset(v);
              void load(action, v);
            }}
            options={[
              { value: "today", label: t("pages.access_logs.range_today") },
              { value: "7d", label: t("pages.access_logs.range_7d") },
              { value: "30d", label: t("pages.access_logs.range_30d") },
              { value: "custom", label: t("pages.access_logs.range_custom") },
            ]}
          />
          {rangePreset === "custom" ? (
            <DatePicker.RangePicker
              showTime
              value={rangeValue}
              onChange={(v) => setRangeValue((v as [Dayjs | null, Dayjs | null]) || null)}
            />
          ) : null}
          <Button onClick={() => void load(action, rangePreset)}>{t("pages.access_logs.refresh")}</Button>
        </Space>
      }
    >
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
        columns={[
          { title: t("pages.access_logs.col_time"), dataIndex: "created_at", width: 180, render: renderServerDateTime },
          { title: t("pages.access_logs.col_user"), dataIndex: "username", width: 120, render: (v?: string) => v || "-" },
          {
            title: t("pages.access_logs.col_event"),
            dataIndex: "action",
            width: 130,
            render: (v: string) => {
              const color = v === "login" ? "green" : v === "logout" ? "orange" : v === "playback_start" ? "blue" : "purple";
              const label =
                v === "login"
                  ? t("pages.access_logs.filter_login")
                  : v === "logout"
                    ? t("pages.access_logs.filter_logout")
                    : v === "playback_start"
                      ? t("pages.access_logs.filter_playback_start")
                      : v === "playback_end"
                        ? t("pages.access_logs.filter_playback_end")
                        : v;
              return <Tag color={color}>{label}</Tag>;
            },
          },
          { title: t("pages.access_logs.col_media_id"), dataIndex: "media_id", width: 100, render: (v: number) => (v > 0 ? v : "-") },
          {
            title: t("pages.access_logs.col_play_progress"),
            key: "play-pos",
            width: 100,
            render: (_: unknown, r: AccessLogItem) => (r.action.startsWith("playback_") ? `${parsePlayback(r.message).pos}s` : "-"),
          },
          {
            title: t("pages.access_logs.col_completed"),
            key: "play-completed",
            width: 80,
            render: (_: unknown, r: AccessLogItem) => {
              if (!r.action.startsWith("playback_")) return "-";
              return parsePlayback(r.message).completed === 1 ? <Tag color="green">{t("pages.access_logs.completed_yes")}</Tag> : <Tag>{t("pages.access_logs.completed_no")}</Tag>;
            },
          },
          { title: t("pages.access_logs.col_browser"), key: "browser", width: 100, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).browser },
          { title: t("pages.access_logs.col_os"), key: "os", width: 100, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).os },
          { title: t("pages.access_logs.col_device"), key: "ua", width: 240, ellipsis: true, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).ua },
          { title: t("pages.access_logs.col_detail"), dataIndex: "message", ellipsis: true },
        ]}
      />
    </Card>
  );
}
