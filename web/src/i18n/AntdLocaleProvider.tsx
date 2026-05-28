import { ConfigProvider, theme } from "antd";
import type { Locale } from "antd/lib/locale";
import enUS from "antd/locale/en_US";
import jaJP from "antd/locale/ja_JP";
import koKR from "antd/locale/ko_KR";
import zhCN from "antd/locale/zh_CN";
import zhTW from "antd/locale/zh_TW";
import { useMemo, type ReactNode } from "react";

import { useI18n } from "./index";

const ANTD_LOCALES: Record<string, Locale> = {
  "zh-CN": zhCN,
  "zh-TW": zhTW,
  en: enUS,
  ja: jaJP,
  ko: koKR,
};

type Props = {
  children: ReactNode;
};

export default function AntdLocaleProvider({ children }: Props) {
  const { locale } = useI18n();
  const antdLocale = useMemo(() => ANTD_LOCALES[locale] ?? zhCN, [locale]);
  return (
    <ConfigProvider locale={antdLocale} theme={{ algorithm: theme.darkAlgorithm }}>
      {children}
    </ConfigProvider>
  );
}
