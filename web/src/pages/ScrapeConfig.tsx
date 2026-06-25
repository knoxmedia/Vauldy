import {
  Button,
  Input,
  message,
  Modal,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  ApiOutlined,
  InfoCircleOutlined,
  SettingOutlined,
} from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import {
  fetchScrapeConfig,
  saveScrapeConfig,
  testScrapeProvider,
} from "../api/client";
import type { ScrapeConfig } from "../api/client";
import { useT, type TranslateFn } from "../i18n";

interface ProviderInfo {
  label: string;
  value: string;
  description: string;
}

function buildProviderOptions(t: TranslateFn): ProviderInfo[] {
  return [
    { label: "TMDb", value: "tmdb", description: t("pages.scrape_config.provider_desc_tmdb") },
    { label: "OMDb", value: "omdb", description: t("pages.scrape_config.provider_desc_omdb") },
    { label: "Bangumi (番组计划)", value: "bangumi", description: t("pages.scrape_config.provider_desc_bangumi") },
    { label: "TVDB", value: "tvdb", description: t("pages.scrape_config.provider_desc_tvdb") },
    { label: "Douban (豆瓣)", value: "douban", description: t("pages.scrape_config.provider_desc_douban") },
    { label: "Fanart.tv", value: "fanart", description: t("pages.scrape_config.provider_desc_fanart") },
    { label: "AI", value: "ai", description: t("pages.scrape_config.provider_desc_ai") },
  ];
}

function providerLabelOf(options: ProviderInfo[], value: string) {
  const found = options.find((o) => o.value === value);
  return found ? found.label : value;
}

interface TableRow {
  key: string;
  value: string;
  label: string;
  description: string;
  configured: boolean;
}

type ProviderTestState =
  | { status: "loading" }
  | { status: "done"; ok: boolean; message: string };

function apiKeyPlaceholder(provider: string, t: TranslateFn) {
  switch (provider) {
    case "tvdb":
      return t("pages.scrape_config.apikey_placeholder_tvdb");
    case "bangumi":
      return t("pages.scrape_config.apikey_placeholder_bangumi");
    default:
      return t("pages.scrape_config.apikey_placeholder_default");
  }
}

export default function ScrapeConfigPage() {
  const t = useT();
  const PROVIDER_OPTIONS = useMemo(() => buildProviderOptions(t), [t]);
  const [loading, setLoading] = useState(true);
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [modalProvider, setModalProvider] = useState<string | null>(null);
  const [modalKey, setModalKey] = useState("");
  const [cfg, setCfg] = useState<ScrapeConfig | null>(null);
  const [infoProvider, setInfoProvider] = useState<ProviderInfo | null>(null);
  const [testResults, setTestResults] = useState<Record<string, ProviderTestState>>({});
  const [testingProvider, setTestingProvider] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    fetchScrapeConfig()
      .then((c: ScrapeConfig) => {
        if (cancelled) return;
        setCfg(c);
        setApiKeys(c.api_keys ?? {});
      })
      .catch(() => {
        if (!cancelled) message.error(t("pages.scrape_config.load_failed"));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
     
  }, []);

  const saveApiKey = async () => {
    if (!modalProvider || !cfg) return;
    const next = { ...apiKeys };
    const trimmed = modalKey.trim();
    if (trimmed) {
      next[modalProvider] = trimmed;
    } else {
      delete next[modalProvider];
    }
    try {
      await saveScrapeConfig({
        enabled: cfg.enabled,
        providers: cfg.providers ?? [],
        image_sources: cfg.image_sources ?? [],
        api_keys: next,
      });
      setApiKeys(next);
      message.success(t("pages.scrape_config.save_success_key", { name: providerLabelOf(PROVIDER_OPTIONS, modalProvider) }));
    } catch {
      message.error(t("pages.scrape_config.save_failed"));
    } finally {
      setModalProvider(null);
      setModalKey("");
    }
  };

  const openApiKeyModal = (provider: string) => {
    setModalProvider(provider);
    setModalKey(apiKeys[provider] ?? "");
  };

  const runProviderTest = async (provider: string) => {
    setTestingProvider(provider);
    setTestResults((prev) => ({ ...prev, [provider]: { status: "loading" } }));
    try {
      const result = await testScrapeProvider(provider);
      setTestResults((prev) => ({
        ...prev,
        [provider]: { status: "done", ok: result.ok, message: result.message },
      }));
    } catch {
      setTestResults((prev) => ({
        ...prev,
        [provider]: { status: "done", ok: false, message: t("pages.scrape_config.test_request_failed") },
      }));
    } finally {
      setTestingProvider(null);
    }
  };

  const dataSource: TableRow[] = PROVIDER_OPTIONS.map((p) => ({
    key: p.value,
    value: p.value,
    label: p.label,
    description: p.description,
    configured: !!apiKeys[p.value],
  }));

  const columns: ColumnsType<TableRow> = [
    {
      title: t("pages.scrape_config.col_provider"),
      dataIndex: "label",
      width: 280,
      render: (label: string, r) => {
        const test = testResults[r.value];
        return (
          <div>
            <div>{label}</div>
            {test?.status === "loading" ? (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                <Spin size="small" style={{ marginRight: 6 }} />
                {t("pages.scrape_config.testing")}
              </Typography.Text>
            ) : test?.status === "done" ? (
              <Typography.Text
                type={test.ok ? "success" : "danger"}
                style={{ fontSize: 12, display: "block", marginTop: 4 }}
              >
                {test.message}
              </Typography.Text>
            ) : null}
          </div>
        );
      },
    },
    {
      title: t("pages.scrape_config.col_api_key"),
      key: "status",
      width: 130,
      render: (_, r) =>
        r.configured ? <Tag color="blue">{t("pages.scrape_config.key_set")}</Tag> : <Tag>{t("pages.scrape_config.key_unset")}</Tag>,
    },
    {
      title: t("pages.scrape_config.col_actions"),
      key: "actions",
      width: 240,
      fixed: "right",
      align: "center",
      onCell: () => ({ style: { whiteSpace: "nowrap" } }),
      render: (_, r) => (
        <Space size={4}>
          <Button
            size="small"
            icon={<InfoCircleOutlined />}
            onClick={() => setInfoProvider(r)}
          >
            {t("pages.scrape_config.btn_view")}
          </Button>
          <Button
            size="small"
            icon={<SettingOutlined />}
            onClick={() => openApiKeyModal(r.value)}
          >
            {t("pages.scrape_config.btn_settings")}
          </Button>
          <Button
            size="small"
            icon={<ApiOutlined />}
            loading={testingProvider === r.value}
            onClick={() => runProviderTest(r.value)}
          >
            {t("pages.scrape_config.btn_test")}
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Table
        rowKey="key"
        loading={loading}
        dataSource={dataSource}
        pagination={false}
        columns={columns}
        scroll={{ x: 650 }}
      />

      <Modal
        title={t("pages.scrape_config.modal_apikey_title", {
          name: modalProvider ? providerLabelOf(PROVIDER_OPTIONS, modalProvider) : "",
        })}
        open={modalProvider !== null}
        onOk={saveApiKey}
        onCancel={() => {
          setModalProvider(null);
          setModalKey("");
        }}
        okText={t("pages.scrape_config.modal_ok")}
        cancelText={t("pages.scrape_config.modal_cancel")}
      >
        <Input.Password
          placeholder={modalProvider ? apiKeyPlaceholder(modalProvider, t) : t("pages.scrape_config.apikey_placeholder_default")}
          value={modalKey}
          onChange={(e) => setModalKey(e.target.value)}
          style={{ marginTop: 8 }}
        />
      </Modal>

      <Modal
        title={infoProvider ? infoProvider.label : ""}
        open={infoProvider !== null}
        onCancel={() => setInfoProvider(null)}
        footer={null}
      >
        {infoProvider && (
          <Typography.Paragraph style={{ fontSize: 14, lineHeight: 1.8 }}>
            {infoProvider.description}
          </Typography.Paragraph>
        )}
      </Modal>
    </div>
  );
}
