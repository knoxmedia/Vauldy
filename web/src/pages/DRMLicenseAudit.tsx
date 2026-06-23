import { Alert, Button, Card, Descriptions, Input, Select, Space, Switch, Table, Tag, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { fetchDRMLicenseAudits, type DRMLicenseAuditItem, type VerifyDRMLicenseResponse, verifyDRMLicense } from "../api/client";
import { renderServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";

export default function DRMLicenseAuditPage() {
  const navigate = useNavigate();
  const t = useT();
  const [rows, setRows] = useState<DRMLicenseAuditItem[]>([]);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [resultFilter, setResultFilter] = useState("all");
  const [mediaFilter, setMediaFilter] = useState("");
  const [typeFilter, setTypeFilter] = useState<"all" | "widevine" | "fairplay">("all");
  const [reasonFilter, setReasonFilter] = useState("");
  const [rangeFilter, setRangeFilter] = useState<"all" | "today" | "7d" | "30d">("7d");
  const [verifyLicense, setVerifyLicense] = useState("");
  const [verifySig, setVerifySig] = useState("");
  const [verifyResult, setVerifyResult] = useState<VerifyDRMLicenseResponse | null>(null);
  const [verifyError, setVerifyError] = useState("");
  const [verifyLoading, setVerifyLoading] = useState(false);

  const load = async () => {
    const mediaID = Number((mediaFilter || "").trim());
    const items = await fetchDRMLicenseAudits({
      limit: 300,
      media_id: Number.isFinite(mediaID) && mediaID > 0 ? mediaID : undefined,
      drm_type: typeFilter,
      result: resultFilter,
      reason: reasonFilter.trim() || undefined,
      range: rangeFilter,
    });
    setRows(items);
  };

  useEffect(() => {
    void load();
     
  }, []);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => void load(), 10000);
    return () => window.clearInterval(timer);
     
  }, [autoRefresh, resultFilter, mediaFilter, typeFilter, reasonFilter, rangeFilter]);

  const filtered = useMemo(
    () => rows.filter((x) => (resultFilter === "all" ? true : x.result === resultFilter)),
    [rows, resultFilter]
  );

  const onVerify = async () => {
    setVerifyError("");
    setVerifyResult(null);
    setVerifyLoading(true);
    try {
      const data = await verifyDRMLicense({ license: verifyLicense.trim(), sig: verifySig.trim() });
      setVerifyResult(data);
    } catch (err) {
      const e = err as { response?: { data?: { error?: string; code?: string } }; message?: string };
      const code = e.response?.data?.code ? `[${e.response?.data?.code}] ` : "";
      setVerifyError(`${code}${e.response?.data?.error || e.message || t("pages.drm_audit.verify_failed")}`);
    } finally {
      setVerifyLoading(false);
    }
  };

  const formatUnixLocal = (unixSec: number) => {
    if (!Number.isFinite(unixSec) || unixSec <= 0) return "-";
    return new Date(unixSec * 1000).toLocaleString();
  };

  const buildCanonicalPreview = (result: VerifyDRMLicenseResponse) => {
    if (result.canonical && result.canonical.trim() !== "") return result.canonical;
    const c = result.claims;
    if (!result.valid || !c) return "";
    return `${c.drm_type}|${c.media_id}|${c.kid}|${c.key_ref}|${c.exp}|${c.nonce}|${c.kid_version}|${c.sig_version}`;
  };

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="middle">
      <Card title={t("pages.drm_audit.verify_title")}>
        <Space direction="vertical" style={{ width: "100%" }} size="small">
          <Input.TextArea
            autoSize={{ minRows: 3, maxRows: 6 }}
            value={verifyLicense}
            onChange={(e) => setVerifyLicense(e.target.value)}
            placeholder={t("pages.drm_audit.verify_license_placeholder")}
          />
          <Input
            value={verifySig}
            onChange={(e) => setVerifySig(e.target.value)}
            placeholder={t("pages.drm_audit.verify_sig_placeholder")}
          />
          <Space>
            <Button
              type="primary"
              loading={verifyLoading}
              onClick={() => void onVerify()}
              disabled={!verifyLicense.trim() || !verifySig.trim()}
            >
              {t("pages.drm_audit.verify_btn")}
            </Button>
          </Space>
          {verifyError ? <Alert type="error" message={verifyError} showIcon /> : null}
          {verifyResult?.valid && verifyResult.claims ? (
            <>
              <Input.TextArea
                readOnly
                autoSize={{ minRows: 2, maxRows: 4 }}
                value={buildCanonicalPreview(verifyResult)}
              />
              <Button
                size="small"
                onClick={() => {
                  void navigator.clipboard.writeText(buildCanonicalPreview(verifyResult));
                  message.success(t("pages.drm_audit.copied_canonical"));
                }}
              >
                {t("pages.drm_audit.copy_canonical")}
              </Button>
              <Descriptions size="small" bordered column={2}>
                <Descriptions.Item label={t("pages.drm_audit.verify_result_label")}>
                  <Tag color="green">valid</Tag>
                </Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.drm_type_label")}>{verifyResult.claims.drm_type}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.media_id_label")}>{verifyResult.claims.media_id}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.kid_label")}>{verifyResult.claims.kid}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.kid_version_label")}>{verifyResult.claims.kid_version}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.sig_version_label")}>{verifyResult.claims.sig_version}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.iat_label")}>{formatUnixLocal(verifyResult.claims.iat)}</Descriptions.Item>
                <Descriptions.Item label={t("pages.drm_audit.exp_label")}>{formatUnixLocal(verifyResult.claims.exp)}</Descriptions.Item>
              </Descriptions>
              <Tag color={verifyResult.claims.exp * 1000 > Date.now() ? "green" : "red"}>
                {verifyResult.claims.exp * 1000 > Date.now() ? t("pages.drm_audit.not_expired") : t("pages.drm_audit.expired")}
              </Tag>
            </>
          ) : null}
        </Space>
      </Card>
      <Card
        title={t("pages.drm_audit.title")}
        extra={
          <Space>
          <Input
            size="small"
            placeholder={t("pages.drm_audit.filter_media_id")}
            style={{ width: 110 }}
            value={mediaFilter}
            onChange={(e) => setMediaFilter(e.target.value)}
          />
          <Select
            size="small"
            value={typeFilter}
            style={{ width: 120 }}
            onChange={(v) => setTypeFilter(v)}
            options={[
              { value: "all", label: "all drm" },
              { value: "widevine", label: "widevine" },
              { value: "fairplay", label: "fairplay" },
            ]}
          />
          <Select
            size="small"
            value={resultFilter}
            style={{ width: 110 }}
            onChange={setResultFilter}
            options={[
              { value: "all", label: "all" },
              { value: "allowed", label: "allowed" },
              { value: "denied", label: "denied" },
              { value: "error", label: "error" },
            ]}
          />
          <Input
            size="small"
            placeholder={t("pages.drm_audit.filter_reason_keyword")}
            style={{ width: 140 }}
            value={reasonFilter}
            onChange={(e) => setReasonFilter(e.target.value)}
          />
          <Select
            size="small"
            value={rangeFilter}
            style={{ width: 95 }}
            onChange={(v) => setRangeFilter(v)}
            options={[
              { value: "today", label: "today" },
              { value: "7d", label: "7d" },
              { value: "30d", label: "30d" },
              { value: "all", label: "all" },
            ]}
          />
          <Space size={4}>
            <span style={{ color: "#999" }}>{t("pages.drm_audit.auto_refresh")}</span>
            <Switch size="small" checked={autoRefresh} onChange={setAutoRefresh} />
          </Space>
          <Button size="small" onClick={() => void load()}>
            {t("pages.drm_audit.refresh")}
          </Button>
          </Space>
        }
      >
        <Table
        rowKey="id"
        dataSource={filtered}
        pagination={{ pageSize: 15 }}
        columns={[
          { title: t("pages.drm_audit.col_id"), dataIndex: "id", width: 80 },
          { title: t("pages.drm_audit.col_media_id"), dataIndex: "media_id", width: 90 },
          { title: t("pages.drm_audit.col_drm"), dataIndex: "drm_type", width: 100 },
          { title: t("pages.drm_audit.col_result"), dataIndex: "result", width: 100 },
          { title: t("pages.drm_audit.col_reason"), dataIndex: "reason", ellipsis: true, render: (v?: string) => v || "-" },
          { title: t("pages.drm_audit.col_client_ip"), dataIndex: "client_ip", width: 140 },
          { title: t("pages.drm_audit.col_time"), dataIndex: "created_at", width: 180, render: renderServerDateTime },
          {
            title: t("pages.drm_audit.col_ops"),
            key: "ops",
            width: 100,
            render: (_: unknown, r: DRMLicenseAuditItem) => (
              <Button size="small" onClick={() => navigate(`/detail/${r.media_id}`)}>
                {t("pages.drm_audit.view_media_detail")}
              </Button>
            ),
          },
        ]}
        />
      </Card>
    </Space>
  );
}
