import { Button, Card, DatePicker, Select, Space, Switch, Table, Tag } from "antd";
import { useEffect, useState } from "react";
import { fetchAccessLogs, type AccessLogItem } from "../api/client";
import { type Dayjs } from "dayjs";

export default function AccessLogsPage() {
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
      title="访问日志"
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
              { value: "all", label: "全部事件" },
              { value: "login", label: "登录" },
              { value: "logout", label: "退出" },
              { value: "playback_start", label: "播放开始" },
              { value: "playback_end", label: "播放结束" },
            ]}
          />
          <Space size={4}>
            <span style={{ color: "#999" }}>仅看播放事件</span>
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
              { value: "today", label: "今天" },
              { value: "7d", label: "近7天" },
              { value: "30d", label: "近30天" },
              { value: "custom", label: "自定义" },
            ]}
          />
          {rangePreset === "custom" ? (
            <DatePicker.RangePicker
              showTime
              value={rangeValue}
              onChange={(v) => setRangeValue((v as [Dayjs | null, Dayjs | null]) || null)}
            />
          ) : null}
          <Button onClick={() => void load(action, rangePreset)}>刷新</Button>
        </Space>
      }
    >
      <Table
        rowKey="id"
        loading={loading}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
        columns={[
          { title: "时间", dataIndex: "created_at", width: 180 },
          { title: "用户", dataIndex: "username", width: 120, render: (v?: string) => v || "-" },
          {
            title: "事件",
            dataIndex: "action",
            width: 130,
            render: (v: string) => {
              const color = v === "login" ? "green" : v === "logout" ? "orange" : v === "playback_start" ? "blue" : "purple";
              const label =
                v === "login"
                  ? "登录"
                  : v === "logout"
                    ? "退出"
                    : v === "playback_start"
                      ? "播放开始"
                      : v === "playback_end"
                        ? "播放结束"
                        : v;
              return <Tag color={color}>{label}</Tag>;
            },
          },
          { title: "媒体ID", dataIndex: "media_id", width: 100, render: (v: number) => (v > 0 ? v : "-") },
          {
            title: "观看进度",
            key: "play-pos",
            width: 100,
            render: (_: unknown, r: AccessLogItem) => (r.action.startsWith("playback_") ? `${parsePlayback(r.message).pos}s` : "-"),
          },
          {
            title: "完播",
            key: "play-completed",
            width: 80,
            render: (_: unknown, r: AccessLogItem) => {
              if (!r.action.startsWith("playback_")) return "-";
              return parsePlayback(r.message).completed === 1 ? <Tag color="green">是</Tag> : <Tag>否</Tag>;
            },
          },
          { title: "浏览器", key: "browser", width: 100, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).browser },
          { title: "系统", key: "os", width: 100, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).os },
          { title: "设备信息", key: "ua", width: 240, ellipsis: true, render: (_: unknown, r: AccessLogItem) => parseDevice(r.message).ua },
          { title: "详细信息", dataIndex: "message", ellipsis: true },
        ]}
      />
    </Card>
  );
}
