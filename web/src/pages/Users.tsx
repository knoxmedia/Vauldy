import { Alert, Button, Checkbox, Divider, Drawer, Form, Input, Modal, Select, Space, Switch, Table, Tabs, Tag, TimePicker, Typography, message } from "antd";
import { MinusCircleOutlined, PlusOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import axios from "axios";
import { useEffect, useState } from "react";
import { createAdminUser, deleteAdminUser, fetchAdminUsers, fetchLibraries, resetAdminUserPassword, type AdminUser, type Library, updateAdminUser } from "../api/client";
import { useT } from "../i18n";

type FormData = {
  username: string;
  password?: string;
  reset_password?: string;
  reset_password_confirm?: string;
  allow_server_manage: boolean;
  can_manage: boolean;
  can_play: boolean;
  can_download: boolean;
  can_access_features: boolean;
  library_scope: "all" | "selected";
  allow_all_libraries?: boolean;
  library_ids: number[];
  library_folders: Record<string, string[]>;
  parental_enabled: boolean;
  parental_max_rating?: string;
  parental_pin?: string;
  parental_plans?: Array<{
    weekday: number;
    start_time?: dayjs.Dayjs;
    end_time?: dayjs.Dayjs;
  }>;
  /** Preserved from API; hidden fields so saves do not wipe DB columns. */
  allowed_time_start?: string;
  allowed_time_end?: string;
};

export default function UsersPage() {
  const t = useT();
  const sectionStyle = {
    padding: 12,
    border: "1px solid #303030",
    borderRadius: 8,
    background: "transparent",
    marginBottom: 14,
  };
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [libraries, setLibraries] = useState<Library[]>([]);
  const [loading, setLoading] = useState(false);
  const [editing, setEditing] = useState<AdminUser | null>(null);
  const [open, setOpen] = useState(false);
  const [activeTab, setActiveTab] = useState("basic");
  const [saving, setSaving] = useState(false);
  const [resettingPassword, setResettingPassword] = useState(false);
  const [form] = Form.useForm<FormData>();
  const parentalEnabledWatch = Form.useWatch("parental_enabled", form);

  const hasParentalPlanConflict = (plans: NonNullable<FormData["parental_plans"]>) => {
    type Seg = { start: number; end: number };
    const byDay: Record<number, Seg[]> = {};
    const toMin = (t?: dayjs.Dayjs) => (t ? t.hour() * 60 + t.minute() : null);
    for (const p of plans) {
      const day = Number(p.weekday);
      const s = toMin(p.start_time);
      const e = toMin(p.end_time);
      if (!Number.isInteger(day) || day < 0 || day > 6 || s === null || e === null) continue;
      if (s === e) return true;
      if (s < e) {
        byDay[day] = [...(byDay[day] || []), { start: s, end: e }];
      } else {
        byDay[day] = [...(byDay[day] || []), { start: s, end: 24 * 60 }];
        const next = (day + 1) % 7;
        byDay[next] = [...(byDay[next] || []), { start: 0, end: e }];
      }
    }
    for (const day of Object.keys(byDay)) {
      const segs = byDay[Number(day)];
      for (let i = 0; i < segs.length; i += 1) {
        for (let j = i + 1; j < segs.length; j += 1) {
          if (segs[i].start < segs[j].end && segs[j].start < segs[i].end) {
            return true;
          }
        }
      }
    }
    return false;
  };

  const load = async () => {
    setLoading(true);
    try {
      const [u, libs] = await Promise.all([fetchAdminUsers(), fetchLibraries()]);
      setUsers(u);
      setLibraries(libs);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.users.load_failed"));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      allow_server_manage: false, can_manage: false, can_play: true, can_download: false, can_access_features: true,
      library_scope: "all", allow_all_libraries: true, library_ids: [], library_folders: {}, parental_enabled: false,
      allowed_time_start: "", allowed_time_end: "",
    });
    setActiveTab("basic");
    setOpen(true);
  };

  const submit = async () => {
    setSaving(true);
    try {
      const v = await form.validateFields();
      // library_ids / library_folders are updated via setFieldValue inside a shouldUpdate render;
      // they are not mounted as named Form.Item fields, so validateFields() does not include them on `v`.
      const library_ids = (form.getFieldValue("library_ids") as number[] | undefined) ?? [];
      const library_folders = (form.getFieldValue("library_folders") as Record<string, string[]> | undefined) ?? {};
      const hasFolderSelection = Object.values(library_folders).some((paths) => Array.isArray(paths) && paths.length > 0);
      const hasLibrarySelection = library_ids.length > 0 || hasFolderSelection;
      if (!v.allow_all_libraries && !hasLibrarySelection) {
        message.error(t("pages.users.library_required"));
        setActiveTab("access");
        setSaving(false);
        return;
      }
      const payload = {
        username: v.username,
        role: v.allow_server_manage ? "admin" : "user",
        can_manage: v.allow_server_manage ? 1 : 0,
        can_play: v.can_play ? 1 : 0,
        can_download: v.can_download ? 1 : 0,
        can_access_features: v.can_access_features ? 1 : 0,
        library_scope: v.allow_all_libraries ? "all" : "selected",
        library_ids,
        library_folders,
        parental_enabled: v.parental_enabled ? 1 : 0,
        parental_max_rating: v.parental_max_rating || "",
        parental_pin: v.parental_pin || "",
        allowed_time_start: (v.allowed_time_start || "").trim(),
        allowed_time_end: (v.allowed_time_end || "").trim(),
        parental_plans: (v.parental_plans || [])
          .map((p) => ({
            weekday: Number(p.weekday),
            start_time: p.start_time ? p.start_time.format("HH:mm") : "",
            end_time: p.end_time ? p.end_time.format("HH:mm") : "",
          }))
          .filter((p) => Number.isInteger(p.weekday) && p.weekday >= 0 && p.weekday <= 6 && p.start_time && p.end_time),
      } as const;
      if (payload.parental_plans.length > 0 && hasParentalPlanConflict(v.parental_plans || [])) {
        message.error(t("pages.users.schedule_conflict"));
        setActiveTab("parental");
        return;
      }
      if (editing) {
        await updateAdminUser(editing.id, payload);
        message.success(t("pages.users.user_updated"));
      } else {
        if (!v.password) {
          message.error(t("pages.users.password_required"));
          return;
        }
        await createAdminUser({ ...payload, password: v.password });
        message.success(t("pages.users.user_created"));
      }
      setOpen(false);
      await load();
    } catch (e: unknown) {
      const msg = axios.isAxiosError(e)
        ? String((e.response?.data as { error?: string } | undefined)?.error || e.message || t("pages.users.save_failed"))
        : String((e as Error)?.message || t("pages.users.save_failed"));
      if (msg.includes("parental plans conflict")) {
        message.error(t("pages.users.schedule_time_conflict"));
        setActiveTab("parental");
      } else {
        message.error(msg);
      }
    } finally {
      setSaving(false);
    }
  };

  const submitResetPassword = async () => {
    if (!editing) {
      message.info(t("pages.users.set_password_in_basic_info"));
      return;
    }
    const pwd = String(form.getFieldValue("reset_password") || "").trim();
    const pwdConfirm = String(form.getFieldValue("reset_password_confirm") || "").trim();
    if (pwd.length < 6) {
      message.error(t("pages.users.password_too_short"));
      return;
    }
    if (pwd !== pwdConfirm) {
      message.error(t("pages.users.password_mismatch"));
      return;
    }
    setResettingPassword(true);
    try {
      await resetAdminUserPassword(editing.id, pwd);
      form.setFieldValue("reset_password", "");
      message.success(t("pages.users.password_reset_success"));
    } finally {
      setResettingPassword(false);
    }
  };

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Button type="primary" onClick={openCreate}>{t("pages.users.create_btn")}</Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={users}
        pagination={false}
        columns={[
          { title: t("pages.users.col_username"), dataIndex: "username" },
          { title: t("pages.users.col_role"), dataIndex: "role", render: (v: string) => <Tag color={v === "admin" ? "blue" : "default"}>{v}</Tag> },
          { title: t("pages.users.col_library"), dataIndex: "library_scope", render: (v: string, r: AdminUser) => v === "all" ? t("pages.users.library_all") : t("pages.users.library_selected", { count: r.library_ids?.length || 0 }) },
          { title: t("pages.users.col_play_download"), render: (_, r: AdminUser) => `${r.can_play ? t("pages.users.yes") : t("pages.users.no")} / ${r.can_download ? t("pages.users.yes") : t("pages.users.no")}` },
          { title: t("pages.users.col_parental"), render: (_, r: AdminUser) => r.parental_enabled ? t("pages.users.parental_on", { rating: r.parental_max_rating || t("pages.users.parental_unlimited") }) : t("pages.users.parental_off") },
          {
            title: t("pages.users.col_actions"),
            render: (_, r: AdminUser) => (
              <Space>
                <Button size="small" onClick={() => {
                  setEditing(r);
                  form.setFieldsValue({
                    username: r.username, allow_server_manage: r.role === "admin" || r.can_manage === 1, can_manage: r.can_manage === 1, can_play: r.can_play === 1, can_download: r.can_download === 1,
                    can_access_features: r.can_access_features === 1, library_scope: r.library_scope, allow_all_libraries: r.library_scope === "all", library_ids: r.library_ids || [],
                    library_folders: r.library_folders || {},
                    parental_enabled: r.parental_enabled === 1, parental_max_rating: r.parental_max_rating || "",
                    parental_plans: (r.parental_plans || []).map((p) => ({
                      weekday: p.weekday,
                      start_time: p.start_time ? dayjs(p.start_time, "HH:mm") : undefined,
                      end_time: p.end_time ? dayjs(p.end_time, "HH:mm") : undefined,
                    })),
                    allowed_time_start: r.allowed_time_start || "",
                    allowed_time_end: r.allowed_time_end || "",
                    reset_password: "",
                    reset_password_confirm: "",
                  });
                  setActiveTab("basic");
                  setOpen(true);
                }}>{t("pages.users.edit")}</Button>
                <Button size="small" danger onClick={() => {
                  Modal.confirm({
                    title: t("pages.users.delete_user_title", { username: r.username }),
                    onOk: async () => {
                      await deleteAdminUser(r.id);
                      message.success(t("pages.users.deleted"));
                      await load();
                    },
                  });
                }}>{t("pages.users.delete")}</Button>
              </Space>
            ),
          },
        ]}
      />

      <Drawer
        title={editing ? t("pages.users.modal_edit_title", { username: editing.username }) : t("pages.users.modal_create_title")}
        open={open}
        width={760}
        onClose={() => setOpen(false)}
        destroyOnClose
        extra={
          <Space>
            <Button onClick={() => setOpen(false)}>{t("pages.users.modal_cancel")}</Button>
            <Button type="primary" loading={saving} onClick={() => void submit()}>{t("pages.users.modal_save")}</Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Form.Item name="allowed_time_start" hidden>
            <Input />
          </Form.Item>
          <Form.Item name="allowed_time_end" hidden>
            <Input />
          </Form.Item>
          <Tabs activeKey={activeTab} onChange={setActiveTab} destroyInactiveTabPane={false} items={[
            {
              key: "basic",
              label: t("pages.users.tab_basic"),
              children: (
                <>
                  <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t("pages.users.alert_basic")} />
                  <div style={sectionStyle}>
                    <Typography.Text strong>{t("pages.users.section_identity")}</Typography.Text>
                    <Divider style={{ margin: "10px 0 14px" }} />
                  <Form.Item name="username" label={t("pages.users.field_username")} rules={[{ required: true }]}><Input /></Form.Item>
                  {!editing && <Form.Item name="password" label={t("pages.users.field_password")} rules={[{ required: true, min: 6 }]}><Input.Password /></Form.Item>}
                  <Form.Item name="allow_server_manage" label={t("pages.users.field_allow_manage")} valuePropName="checked" initialValue={false}>
                    <Switch checkedChildren={t("pages.users.switch_admin")} unCheckedChildren={t("pages.users.switch_user")} />
                  </Form.Item>
                  </div>
                  <div style={sectionStyle}>
                    <Typography.Text strong>{t("pages.users.section_action_perms")}</Typography.Text>
                    <Divider style={{ margin: "10px 0 14px" }} />
                    <Space wrap>
                      <Form.Item name="can_play" label={t("pages.users.field_can_play")} valuePropName="checked"><Switch /></Form.Item>
                      <Form.Item name="can_download" label={t("pages.users.field_can_download")} valuePropName="checked"><Switch /></Form.Item>
                      <Form.Item name="can_access_features" label={t("pages.users.field_can_access_features")} valuePropName="checked"><Switch /></Form.Item>
                    </Space>
                  </div>
                </>
              ),
            },
            {
              key: "access",
              label: t("pages.users.tab_access"),
              children: (
                <>
                  <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t("pages.users.alert_access")} />
                  <div style={sectionStyle}>
                    <Typography.Title level={4} style={{ margin: 0 }}>{t("pages.users.section_library_access")}</Typography.Title>
                    <Divider style={{ margin: "10px 0 14px" }} />
                    <Form.Item label={t("pages.users.field_allow_all_libraries")} style={{ marginBottom: 10 }}>
                      <Form.Item name="allow_all_libraries" valuePropName="checked" noStyle initialValue>
                        <Switch
                          checkedChildren={t("pages.users.switch_all")}
                          unCheckedChildren={t("pages.users.switch_custom")}
                          onChange={(checked) => {
                            if (checked) {
                              form.setFieldValue("library_ids", []);
                              form.setFieldValue("library_folders", {});
                            }
                          }}
                        />
                      </Form.Item>
                    </Form.Item>
                    <Form.Item shouldUpdate noStyle>
                      {({ getFieldValue, setFieldValue }) => {
                        const all = !!getFieldValue("allow_all_libraries");
                        const selected: number[] = getFieldValue("library_ids") || [];
                        const selectedFolders: Record<string, string[]> = getFieldValue("library_folders") || {};
                        if (all) return null;
                        return (
                          <div>
                            <Typography.Text strong>{t("pages.users.library_label")}</Typography.Text>
                            <div style={{ marginTop: 10 }}>
                              {libraries.map((lib) => {
                                const checked = selected.includes(lib.id);
                                const folders = lib.folders && lib.folders.length > 0 ? lib.folders : [lib.path];
                                const libKey = String(lib.id);
                                const pickedFolders = selectedFolders[libKey] || [];
                                const allFoldersChecked = folders.length > 0 && folders.every((f) => pickedFolders.includes(f));
                                const indeterminate = pickedFolders.length > 0 && !allFoldersChecked;
                                return (
                                  <div key={lib.id} style={{ marginBottom: 10 }}>
                                    <Checkbox
                                      checked={checked && allFoldersChecked}
                                      indeterminate={checked && indeterminate}
                                      onChange={(e) => {
                                        const isChecked = e.target.checked;
                                        const nextIDs = isChecked
                                          ? Array.from(new Set([...selected, lib.id]))
                                          : selected.filter((id) => id !== lib.id);
                                        const nextFolders = { ...selectedFolders };
                                        if (isChecked) {
                                          nextFolders[libKey] = [...folders];
                                          form.setFieldValue("allow_all_libraries", false);
                                        } else {
                                          delete nextFolders[libKey];
                                        }
                                        setFieldValue("library_ids", nextIDs);
                                        setFieldValue("library_folders", nextFolders);
                                      }}
                                    >
                                      <Typography.Text strong>{lib.name}</Typography.Text>
                                    </Checkbox>
                                    <div style={{ marginLeft: 26, marginTop: 6 }}>
                                      {folders.map((folder) => (
                                        <div key={`${lib.id}-${folder}`} style={{ marginBottom: 4 }}>
                                          <Checkbox
                                            checked={pickedFolders.includes(folder)}
                                            onChange={(e) => {
                                              const isChecked = e.target.checked;
                                              const current = selectedFolders[libKey] || [];
                                              const nextFolderList = isChecked
                                                ? Array.from(new Set([...current, folder]))
                                                : current.filter((it) => it !== folder);
                                              const nextFolders = { ...selectedFolders };
                                              if (nextFolderList.length > 0) {
                                                nextFolders[libKey] = nextFolderList;
                                              } else {
                                                delete nextFolders[libKey];
                                              }
                                              const nextIDs = nextFolderList.length > 0
                                                ? Array.from(new Set([...selected, lib.id]))
                                                : selected.filter((id) => id !== lib.id);
                                              if (nextFolderList.length > 0) {
                                                form.setFieldValue("allow_all_libraries", false);
                                              }
                                              setFieldValue("library_folders", nextFolders);
                                              setFieldValue("library_ids", nextIDs);
                                            }}
                                          >
                                            <Typography.Text type="secondary">{folder}</Typography.Text>
                                          </Checkbox>
                                        </div>
                                      ))}
                                    </div>
                                  </div>
                                );
                              })}
                            </div>
                            <Typography.Text type="secondary">
                              {t("pages.users.folders_hint")}
                            </Typography.Text>
                          </div>
                        );
                      }}
                    </Form.Item>
                  </div>
                  <div style={sectionStyle}>
                    <Typography.Title level={4} style={{ margin: 0 }}>{t("pages.users.section_device_access")}</Typography.Title>
                    <Divider style={{ margin: "10px 0 14px" }} />
                    <Form.Item label={t("pages.users.all_label")}>
                      <Form.Item name="can_access_features" valuePropName="checked" noStyle>
                        <Switch />
                      </Form.Item>
                    </Form.Item>
                    <Typography.Text type="secondary">
                      {t("pages.users.features_disabled_hint")}
                    </Typography.Text>
                  </div>
                </>
              ),
            },
            {
              key: "parental",
              label: t("pages.users.tab_parental"),
              children: (
                <>
                  <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={t("pages.users.alert_parental")} />
                  <div style={sectionStyle}>
                    <Typography.Text strong>{t("pages.users.section_restrictions")}</Typography.Text>
                    <Divider style={{ margin: "10px 0 14px" }} />
                  <Form.Item name="parental_enabled" label={t("pages.users.field_parental_enabled")} valuePropName="checked"><Switch /></Form.Item>
                  <Form.Item shouldUpdate noStyle>
                    {({ getFieldValue }) =>
                      getFieldValue("parental_enabled") ? (
                        <>
                          <Form.Item name="parental_max_rating" label={t("pages.users.field_max_rating")}>
                            <Select allowClear options={["G", "PG", "PG-13", "R", "NC-17"].map((x) => ({ value: x, label: x }))} />
                          </Form.Item>
                          <Form.Item name="parental_pin" label={t("pages.users.field_pin")}><Input.Password /></Form.Item>
                        </>
                      ) : null}
                  </Form.Item>
                  <Divider style={{ margin: "10px 0 14px" }} />
                  <Typography.Text strong>{t("pages.users.section_schedule")}</Typography.Text>
                  <Typography.Paragraph type="secondary" style={{ margin: "4px 0 10px" }}>
                    {t("pages.users.schedule_hint")}
                  </Typography.Paragraph>
                  <div style={{ display: parentalEnabledWatch ? "block" : "none" }}>
                    <Form.List name="parental_plans">
                      {(fields, { add, remove }) => (
                        <>
                          {fields.map((field) => (
                            <Space key={field.key} align="baseline" wrap style={{ display: "flex", marginBottom: 8 }}>
                              <Form.Item
                                {...field}
                                name={[field.name, "weekday"]}
                                label={t("pages.users.field_weekdays")}
                                dependencies={["parental_enabled"]}
                                rules={[
                                  ({ getFieldValue }) => ({
                                    validator(_, value) {
                                      if (!getFieldValue("parental_enabled")) return Promise.resolve();
                                      const n = Number(value);
                                      if (!Number.isInteger(n) || n < 0 || n > 6) {
                                        return Promise.reject(new Error(t("pages.users.weekdays_required")));
                                      }
                                      return Promise.resolve();
                                    },
                                  }),
                                ]}
                              >
                                <Select
                                  style={{ width: 120 }}
                                  options={[
                                    { value: 0, label: t("pages.users.weekday_sun") },
                                    { value: 1, label: t("pages.users.weekday_mon") },
                                    { value: 2, label: t("pages.users.weekday_tue") },
                                    { value: 3, label: t("pages.users.weekday_wed") },
                                    { value: 4, label: t("pages.users.weekday_thu") },
                                    { value: 5, label: t("pages.users.weekday_fri") },
                                    { value: 6, label: t("pages.users.weekday_sat") },
                                  ]}
                                />
                              </Form.Item>
                              <Form.Item
                                {...field}
                                name={[field.name, "start_time"]}
                                label={t("pages.users.field_start")}
                                dependencies={["parental_enabled"]}
                                rules={[
                                  ({ getFieldValue }) => ({
                                    validator(_, value) {
                                      if (!getFieldValue("parental_enabled")) return Promise.resolve();
                                      if (!value) return Promise.reject(new Error(t("pages.users.start_required")));
                                      return Promise.resolve();
                                    },
                                  }),
                                ]}
                              >
                                <TimePicker format="HH:mm" minuteStep={5} />
                              </Form.Item>
                              <Form.Item
                                {...field}
                                name={[field.name, "end_time"]}
                                label={t("pages.users.field_end")}
                                dependencies={["parental_enabled"]}
                                rules={[
                                  ({ getFieldValue }) => ({
                                    validator(_, value) {
                                      if (!getFieldValue("parental_enabled")) return Promise.resolve();
                                      if (!value) return Promise.reject(new Error(t("pages.users.end_required")));
                                      return Promise.resolve();
                                    },
                                  }),
                                ]}
                              >
                                <TimePicker format="HH:mm" minuteStep={5} />
                              </Form.Item>
                              <Button icon={<MinusCircleOutlined />} onClick={() => remove(field.name)} />
                            </Space>
                          ))}
                          <Button type="dashed" icon={<PlusOutlined />} onClick={() => add({ weekday: 1 })}>
                            {t("pages.users.add_schedule")}
                          </Button>
                        </>
                      )}
                    </Form.List>
                  </div>
                  </div>
                </>
              ),
            },
            {
              key: "reset-password",
              label: t("pages.users.tab_reset_password"),
              children: (
                <>
                  {editing ? (
                    <div style={sectionStyle}>
                      <Typography.Text strong>{t("pages.users.section_reset_password")}</Typography.Text>
                      <Divider style={{ margin: "10px 0 14px" }} />
                    <Space direction="vertical" style={{ width: "100%" }}>
                      <Form.Item name="reset_password" label={t("pages.users.field_new_password")}>
                        <Input.Password />
                      </Form.Item>
                      <Form.Item
                        name="reset_password_confirm"
                        label={t("pages.users.field_confirm_password")}
                        dependencies={["reset_password"]}
                        rules={[
                          ({ getFieldValue }) => ({
                            validator(_, value) {
                              const p1 = String(getFieldValue("reset_password") || "");
                              const p2 = String(value || "");
                              if (!p2 || p1 === p2) return Promise.resolve();
                              return Promise.reject(new Error(t("pages.users.confirm_mismatch_error")));
                            },
                          }),
                        ]}
                      >
                        <Input.Password />
                      </Form.Item>
                      <Button type="primary" loading={resettingPassword} onClick={() => void submitResetPassword()}>
                        {t("pages.users.btn_reset_password")}
                      </Button>
                    </Space>
                    </div>
                  ) : (
                    <Tag color="blue">{t("pages.users.tag_create_password_hint")}</Tag>
                  )}
                </>
              ),
            },
          ]} />
        </Form>
      </Drawer>
    </div>
  );
}

