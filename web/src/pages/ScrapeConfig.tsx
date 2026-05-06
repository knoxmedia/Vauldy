import {
  Button,
  Input,
  message,
  Modal,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { InfoCircleOutlined, SettingOutlined } from "@ant-design/icons";
import { useEffect, useState } from "react";
import { fetchScrapeConfig, saveScrapeConfig } from "../api/client";
import type { ScrapeConfig } from "../api/client";

interface ProviderInfo {
  label: string;
  value: string;
  description: string;
}

const PROVIDER_OPTIONS: ProviderInfo[] = [
  {
    label: "TMDb",
    value: "tmdb",
    description:
      "The Movie Database (TMDb) 是一个由社区维护的电影和电视剧数据库，提供丰富的元数据，包括海报、背景图、演员信息、评分等。支持多语言，是 Kodi、Plex、Jellyfin 等媒体中心的首选元数据源。",
  },
  {
    label: "OMDb",
    value: "omdb",
    description:
      "The Open Movie Database (OMDb) 是一个 RESTful Web 服务，用于获取电影和电视剧的元数据信息。基于 IMDb 数据，提供标题、年份、评分、演员、剧情简介等。需要 API Key 使用。",
  },
  {
    label: "Bangumi (番组计划)",
    value: "bangumi",
    description:
      "Bangumi 番组计划是一个专注于 ACGN（动画、漫画、游戏、小说）领域的中文数据库和社区。提供详细的动画剧集信息、角色、制作人员等元数据，特别适合日本动画的刮削。",
  },
  {
    label: "TVDB",
    value: "tvdb",
    description:
      "TheTVDB.com 是一个开放的电视剧数据库，提供完整的剧集指南、季信息、演员、海报、横幅等。是电视剧媒体管理的权威源，支持多语言剧集信息。需 API Key（订阅制）。",
  },
  {
    label: "Douban (豆瓣)",
    value: "douban",
    description:
      "豆瓣是一个中文社区网站，提供电影、电视剧、书籍、音乐等文化产品的信息和用户评论。豆瓣电影数据库包含丰富的中文元数据、评分、影评，特别适合华语内容的刮削。",
  },
  {
    label: "Fanart.tv",
    value: "fanart",
    description:
      "Fanart.tv 是一个高质量的艺术作品数据库，专注于提供电影和电视剧的粉丝艺术作品，包括高清海报、背景图、Logo、缩略图等图像资源。可与其他元数据提供者配合使用。",
  },
  {
    label: "AI",
    value: "ai",
    description:
      "AI 智能识别使用大语言模型和计算机视觉技术，自动从视频文件名、画面内容中推断元数据。无需外部 API，支持模糊名称匹配和未知媒体的智能归类。适合无法被传统提供者识别的媒体。",
  },
];

function providerLabel(value: string) {
  const found = PROVIDER_OPTIONS.find((o) => o.value === value);
  return found ? found.label : value;
}

interface TableRow {
  key: string;
  value: string;
  label: string;
  description: string;
  configured: boolean;
}

export default function ScrapeConfigPage() {
  const [loading, setLoading] = useState(true);
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [modalProvider, setModalProvider] = useState<string | null>(null);
  const [modalKey, setModalKey] = useState("");
  const [cfg, setCfg] = useState<ScrapeConfig | null>(null);
  const [infoProvider, setInfoProvider] = useState<ProviderInfo | null>(null);

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
        if (!cancelled) message.error("加载配置失败");
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
      message.success(`${providerLabel(modalProvider)} API Key 已保存`);
    } catch {
      message.error("保存失败");
    } finally {
      setModalProvider(null);
      setModalKey("");
    }
  };

  const openApiKeyModal = (provider: string) => {
    setModalProvider(provider);
    setModalKey(apiKeys[provider] ?? "");
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
      title: "提供者",
      dataIndex: "label",
      width: 220,
    },
    {
      title: "API Key",
      key: "status",
      width: 130,
      render: (_, r) =>
        r.configured ? <Tag color="blue">已设置</Tag> : <Tag>未设置</Tag>,
    },
    {
      title: "操作",
      key: "actions",
      width: 160,
      align: "center",
      render: (_, r) => (
        <Space>
          <Button
            size="small"
            icon={<InfoCircleOutlined />}
            onClick={() => setInfoProvider(r)}
          >
            查看
          </Button>
          <Button
            size="small"
            icon={<SettingOutlined />}
            onClick={() => openApiKeyModal(r.value)}
          >
            设置
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
      />

      <Modal
        title={`API Key — ${modalProvider ? providerLabel(modalProvider) : ""}`}
        open={modalProvider !== null}
        onOk={saveApiKey}
        onCancel={() => {
          setModalProvider(null);
          setModalKey("");
        }}
        okText="确定"
        cancelText="取消"
      >
        <Input.Password
          placeholder="输入 API Key"
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
