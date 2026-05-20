import { Form, Tabs } from "antd";
import ProviderPriorityPicker from "./ProviderPriorityPicker";
import {
  DEFAULT_IMAGE_PROVIDERS,
  DEFAULT_METADATA_PROVIDERS,
  IMAGE_PROVIDER_OPTIONS,
  METADATA_PROVIDER_OPTIONS,
  normalizeProviderList,
} from "../lib/scrapeProviders";
import styles from "./LibraryProviderSourceTabs.module.css";

type LibraryProviderSourceTabsProps = {
  activeKey?: string;
  onChange?: (key: string) => void;
};

export default function LibraryProviderSourceTabs({ activeKey, onChange }: LibraryProviderSourceTabsProps) {
  return (
    <Tabs
      className={styles.tabs}
      activeKey={activeKey}
      onChange={onChange}
      items={[
        {
          key: "metadata",
          label: "元数据下载源",
          children: (
            <Form.Item
              name="metadata_providers"
              initialValue={DEFAULT_METADATA_PROVIDERS}
              rules={[
                {
                  validator: async (_, value) => {
                    if (normalizeProviderList(value).length === 0) {
                      throw new Error("请至少选择一个元数据下载源");
                    }
                  },
                },
              ]}
              className={styles.tabField}
            >
              <ProviderPriorityPicker
                options={METADATA_PROVIDER_OPTIONS}
                hint="勾选需要的元数据下载源，拖动右侧手柄调整优先级。优先级较低的源仅用于填补缺失信息。"
              />
            </Form.Item>
          ),
        },
        {
          key: "image",
          label: "图片获取源",
          children: (
            <Form.Item
              name="image_providers"
              initialValue={DEFAULT_IMAGE_PROVIDERS}
              rules={[
                {
                  validator: async (_, value) => {
                    if (normalizeProviderList(value).length === 0) {
                      throw new Error("请至少选择一个图片获取源");
                    }
                  },
                },
              ]}
              className={styles.tabField}
            >
              <ProviderPriorityPicker
                options={IMAGE_PROVIDER_OPTIONS}
                hint="勾选需要的图片获取源，拖动右侧手柄调整优先级。优先级较低的源仅用于填补缺失海报或背景。"
              />
            </Form.Item>
          ),
        },
      ]}
    />
  );
}
