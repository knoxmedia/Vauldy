import { Button, Form, Input, Modal, Select, Space, Upload as AntUpload, message } from "antd";
import { InboxOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import { api, createUploadDirectory, fetchLibraries, fetchMedia, type Library } from "../api/client";
import { useT } from "../i18n";

type UploadTargetOption = {
  value: string;
  label: string;
  libraryId: number;
  fullPath: string;
};

export default function UploadPage() {
  const t = useT();
  const [form] = Form.useForm();
  const [libs, setLibs] = useState<Library[]>([]);
  const [targetOptions, setTargetOptions] = useState<UploadTargetOption[]>([]);
  const [selectedTargetValue, setSelectedTargetValue] = useState<string | undefined>(undefined);
  const [creatingDir, setCreatingDir] = useState(false);
  const [newDirOpen, setNewDirOpen] = useState(false);
  const [newDirName, setNewDirName] = useState("");
  const selectedTarget = useMemo(
    () => targetOptions.find((x) => x.value === selectedTargetValue),
    [targetOptions, selectedTargetValue]
  );

  useEffect(() => {
    if (!selectedTargetValue) return;
    if (!targetOptions.some((x) => x.value === selectedTargetValue)) {
      setSelectedTargetValue(undefined);
      form.setFieldValue("upload_target", undefined);
    }
  }, [selectedTargetValue, targetOptions, form]);

  useEffect(() => {
    const load = async () => {
      const libraries = await fetchLibraries();
      setLibs(libraries);
      const allOptions: UploadTargetOption[] = [];
      await Promise.all(
        libraries.map(async (lib) => {
          const roots = Array.from(new Set([...(lib.folders || []), lib.path].filter(Boolean)));
          const media = await fetchMedia(lib.id, { limit: 5000 });
          const dirSet = new Set<string>();
          roots.forEach((r) => dirSet.add(r));
          media.forEach((m) => {
            const full = (m.file_path || "").replace(/\\/g, "/");
            roots.forEach((root) => {
              const rootNorm = root.replace(/\\/g, "/").replace(/\/+$/, "");
              if (full.toLowerCase().startsWith(`${rootNorm.toLowerCase()}/`)) {
                const rel = full.slice(rootNorm.length + 1);
                const parts = rel.split("/").filter(Boolean);
                let acc = rootNorm;
                parts.slice(0, -1).forEach((p) => {
                  acc = `${acc}/${p}`;
                  dirSet.add(acc);
                });
              }
            });
          });
          const sorted = Array.from(dirSet).sort((a, b) => a.localeCompare(b));
          sorted.forEach((d) => {
            const relative = roots
              .map((r) => r.replace(/\\/g, "/").replace(/\/+$/, ""))
              .sort((a, b) => b.length - a.length)
              .find((r) => d.toLowerCase().startsWith(r.toLowerCase()));
            const short = relative
              ? (d.length === relative.length ? t("pages.upload.root_dir") : d.slice(relative.length + 1))
              : d;
            allOptions.push({
              value: `${lib.id}|${d}`,
              libraryId: lib.id,
              fullPath: d,
              label: `${lib.name} / ${short}`,
            });
          });
        })
      );
      setTargetOptions(allOptions);
    };
    void load().catch((e: unknown) => message.error((e as Error).message || t("pages.upload.load_failed")));
  }, [t]);

  return (
    <div className="app-narrow-block">
      <Form form={form} layout="vertical">
        <Form.Item name="upload_target" label={t("pages.upload.target_label")}>
          <Space.Compact style={{ width: "100%" }}>
            <Select
              allowClear
              showSearch
              style={{ width: "100%" }}
              placeholder={t("pages.upload.target_placeholder")}
              value={selectedTargetValue}
              onChange={(v) => {
                const next = (v as string | undefined) || undefined;
                setSelectedTargetValue(next);
                form.setFieldValue("upload_target", next);
              }}
              options={targetOptions}
              optionFilterProp="label"
            />
            <Button
              onClick={() => {
                if (!selectedTarget) {
                  message.warning(t("pages.upload.pick_parent_first"));
                  return;
                }
                setNewDirName("");
                setNewDirOpen(true);
              }}
            >
              {t("pages.upload.create_dir_btn")}
            </Button>
          </Space.Compact>
        </Form.Item>
        <Form.Item label={t("pages.upload.single_file_upload")}>
          <AntUpload.Dragger
            name="file"
            multiple={false}
            customRequest={async (opt) => {
              try {
                const fd = new FormData();
                fd.append("file", opt.file as File);
                if (selectedTarget) {
                  fd.append("library_id", String(selectedTarget.libraryId));
                  fd.append("target_dir", selectedTarget.fullPath);
                }
                await api.post("/api/v1/upload", fd, {
                  headers: { "Content-Type": "multipart/form-data" },
                });
                message.success(t("pages.upload.upload_complete"));
                opt.onSuccess?.({}, new XMLHttpRequest());
              } catch (e: unknown) {
                message.error((e as Error).message || t("pages.upload.upload_failed"));
                opt.onError?.(e as Error);
              }
            }}
          >
            <p className="ant-upload-drag-icon">
              <InboxOutlined />
            </p>
            <p className="ant-upload-text">{t("pages.upload.drag_hint")}</p>
          </AntUpload.Dragger>
        </Form.Item>
      </Form>
      <Modal
        title={t("pages.upload.create_dir_title")}
        open={newDirOpen}
        onCancel={() => setNewDirOpen(false)}
        onOk={() => {
          if (!selectedTarget) return;
          const name = newDirName.trim();
          if (!name) {
            message.warning(t("pages.upload.dir_name_required"));
            return;
          }
          setCreatingDir(true);
          void createUploadDirectory({
            library_id: selectedTarget.libraryId,
            target_dir: selectedTarget.fullPath,
            name,
          })
            .then((res) => {
              const fullPath = `${selectedTarget.fullPath.replace(/\/+$/, "")}/${name}`;
              const next: UploadTargetOption = {
                value: `${selectedTarget.libraryId}|${fullPath}`,
                libraryId: selectedTarget.libraryId,
                fullPath,
                label: `${libs.find((x) => x.id === selectedTarget.libraryId)?.name || t("pages.upload.library_fallback")} / ${fullPath}`,
              };
              setTargetOptions((prev) => {
                if (prev.some((x) => x.value === next.value)) return prev;
                return [...prev, next].sort((a, b) => a.label.localeCompare(b.label));
              });
              setSelectedTargetValue(next.value);
              form.setFieldValue("upload_target", next.value);
              setNewDirOpen(false);
              message.success(t("pages.upload.dir_created"));
              return res;
            })
            .catch((e: unknown) => message.error((e as Error).message || t("pages.upload.dir_create_failed")))
            .finally(() => setCreatingDir(false));
        }}
        confirmLoading={creatingDir}
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Input value={selectedTarget?.fullPath || ""} disabled />
          <Input
            placeholder={t("pages.upload.new_dir_placeholder")}
            value={newDirName}
            onChange={(e) => setNewDirName(e.target.value)}
          />
        </Space>
      </Modal>
    </div>
  );
}
