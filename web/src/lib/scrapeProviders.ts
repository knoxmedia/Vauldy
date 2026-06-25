export type ScrapeProviderOption = {
  value: string;
  label: string;
  description?: string;
};

/** Metadata download providers (matches backend scraper). */
export const METADATA_PROVIDER_OPTIONS: ScrapeProviderOption[] = [
  {
    value: "tmdb",
    label: "TheMovieDb",
    description: "社区维护的电影与电视剧数据库，提供海报、演员、评分等元数据。",
  },
  {
    value: "omdb",
    label: "The Open Movie Database",
    description: "基于 IMDb 的开放电影数据库，提供标题、评分、剧情等。需 API Key。",
  },
  {
    value: "tvdb",
    label: "TheTVDB",
    description: "开放的电视剧数据库，提供季集、演员、海报等，适合剧集元数据。",
  },
  {
    value: "bangumi",
    label: "Bangumi（番组计划）",
    description: "专注 ACGN 的中文数据库，适合日本动画刮削。",
  },
  {
    value: "douban",
    label: "豆瓣",
    description: "华语电影、电视剧社区数据库，适合中文元数据与评分。",
  },
  {
    value: "fanart",
    label: "Fanart.tv",
    description: "高质量粉丝艺术作品（海报、背景、Logo 等），常作图像补充源。",
  },
  {
    value: "ai",
    label: "AI 智能识别",
    description: "通过 AI 从文件名或画面推断元数据，适合无法匹配传统源的内容。",
  },
];

export const DEFAULT_METADATA_PROVIDERS = ["tmdb", "omdb"];

/** Image download providers (matches library.image_providers default). */
export const IMAGE_PROVIDER_OPTIONS: ScrapeProviderOption[] = [
  {
    value: "tmdb",
    label: "TheMovieDb",
    description: "从 TMDb 获取海报、背景图等官方图像资源。",
  },
  {
    value: "omdb",
    label: "The Open Movie Database",
    description: "从 OMDb 获取海报等图像，需 API Key。",
  },
  {
    value: "fanart",
    label: "Fanart.tv",
    description: "高质量粉丝艺术海报、背景与 Logo，适合补充官方海报。",
  },
  {
    value: "tvdb",
    label: "TheTVDB",
    description: "电视剧海报、横幅与季集图像。",
  },
  {
    value: "embedded",
    label: "内嵌图片",
    description: "从媒体文件内嵌封面或附件图像中提取。",
  },
  {
    value: "screen_grabber",
    label: "画面截图",
    description: "从视频画面中自动截取代表性帧作为封面。",
  },
];

export const DEFAULT_IMAGE_PROVIDERS = ["tmdb", "omdb", "embedded", "screen_grabber"];

const ALL_PROVIDER_OPTIONS = [...METADATA_PROVIDER_OPTIONS, ...IMAGE_PROVIDER_OPTIONS];

export function providerLabel(value: string, options = ALL_PROVIDER_OPTIONS): string {
  return options.find((o) => o.value === value)?.label ?? value;
}

export function normalizeProviderList(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value.map((x) => String(x).trim()).filter(Boolean);
  }
  return String(value ?? "")
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
}
