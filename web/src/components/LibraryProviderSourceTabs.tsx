import { Form, Tabs } from "antd";
import ProviderPriorityPicker from "./ProviderPriorityPicker";
import {
  DEFAULT_IMAGE_PROVIDERS,
  DEFAULT_METADATA_PROVIDERS,
  IMAGE_PROVIDER_OPTIONS,
  METADATA_PROVIDER_OPTIONS,
  normalizeProviderList,
} from "../lib/scrapeProviders";
import { useT } from "../i18n";
import styles from "./LibraryProviderSourceTabs.module.css";

type LibraryProviderSourceTabsProps = {
  activeKey?: string;
  onChange?: (key: string) => void;
};

export default function LibraryProviderSourceTabs({ activeKey, onChange }: LibraryProviderSourceTabsProps) {
  const t = useT();
  return (
    <Tabs
      className={styles.tabs}
      activeKey={activeKey}
      onChange={onChange}
      items={[
        {
          key: "metadata",
          label: t("components.library_provider_source_tabs.tab_metadata"),
          children: (
            <Form.Item
              name="metadata_providers"
              initialValue={DEFAULT_METADATA_PROVIDERS}
              rules={[
                {
                  validator: async (_, value) => {
                    if (normalizeProviderList(value).length === 0) {
                      throw new Error(t("components.library_provider_source_tabs.metadata_required"));
                    }
                  },
                },
              ]}
              className={styles.tabField}
            >
              <ProviderPriorityPicker
                options={METADATA_PROVIDER_OPTIONS}
                hint={t("components.library_provider_source_tabs.metadata_hint")}
              />
            </Form.Item>
          ),
        },
        {
          key: "image",
          label: t("components.library_provider_source_tabs.tab_image"),
          children: (
            <Form.Item
              name="image_providers"
              initialValue={DEFAULT_IMAGE_PROVIDERS}
              rules={[
                {
                  validator: async (_, value) => {
                    if (normalizeProviderList(value).length === 0) {
                      throw new Error(t("components.library_provider_source_tabs.image_required"));
                    }
                  },
                },
              ]}
              className={styles.tabField}
            >
              <ProviderPriorityPicker
                options={IMAGE_PROVIDER_OPTIONS}
                hint={t("components.library_provider_source_tabs.image_hint")}
              />
            </Form.Item>
          ),
        },
      ]}
    />
  );
}
