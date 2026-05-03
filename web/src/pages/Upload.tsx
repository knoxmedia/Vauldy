import { Button, Form, Input, Modal, Select, Space, Upload as AntUpload, message } from "antd";
import { InboxOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import { api, createUploadDirectory, fetchLibraries, fetchMedia, type Library } from "../api/client";

type UploadTargetOption = {
  value: string;
  label: string;
  libraryId: number;
  fullPath: string;
};

export default function UploadPage() {
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
              ? (d.length === relative.length ? "根目录" : d.slice(relative.length + 1))
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
    void load().catch((e: unknown) => message.error((e as Error).message || "加载媒体库目录失败"));
  }, []);

  return (
    <div className="app-narrow-block">
      <Form form={form} layout="vertical">
        <Form.Item name="upload_target" label="上传目录（可选）">
          <Space.Compact style={{ width: "100%" }}>
            <Select
              allowClear
              showSearch
              style={{ width: "100%" }}
              placeholder="选择媒体库与目录；留空则仅保存到默认上传目录"
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
                  message.warning("请先选择一个目录作为父目录");
                  return;
                }
                setNewDirName("");
                setNewDirOpen(true);
              }}
            >
              新建目录
            </Button>
          </Space.Compact>
        </Form.Item>
        <Form.Item label="单文件上传">
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
                message.success("上传完成");
                opt.onSuccess?.({}, new XMLHttpRequest());
              } catch (e: unknown) {
                message.error((e as Error).message || "上传失败");
                opt.onError?.(e as Error);
              }
            }}
          >
            <p className="ant-upload-drag-icon">
              <InboxOutlined />
            </p>
            <p className="ant-upload-text">点击或拖拽文件到此处</p>
          </AntUpload.Dragger>
        </Form.Item>
      </Form>
      <Modal
        title="新建目录"
        open={newDirOpen}
        onCancel={() => setNewDirOpen(false)}
        onOk={() => {
          if (!selectedTarget) return;
          const name = newDirName.trim();
          if (!name) {
            message.warning("请输入目录名");
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
                label: `${libs.find((x) => x.id === selectedTarget.libraryId)?.name || "媒体库"} / ${fullPath}`,
              };
              setTargetOptions((prev) => {
                if (prev.some((x) => x.value === next.value)) return prev;
                return [...prev, next].sort((a, b) => a.label.localeCompare(b.label));
              });
              setSelectedTargetValue(next.value);
              form.setFieldValue("upload_target", next.value);
              setNewDirOpen(false);
              message.success("目录已创建");
              return res;
            })
            .catch((e: unknown) => message.error((e as Error).message || "创建目录失败"))
            .finally(() => setCreatingDir(false));
        }}
        confirmLoading={creatingDir}
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Input value={selectedTarget?.fullPath || ""} disabled />
          <Input
            placeholder="输入新目录名"
            value={newDirName}
            onChange={(e) => setNewDirName(e.target.value)}
          />
        </Space>
      </Modal>
    </div>
  );
}
