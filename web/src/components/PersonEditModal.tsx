import { CameraOutlined, CloudDownloadOutlined } from "@ant-design/icons";
import { Button, DatePicker, Drawer, Input, Modal, Select, Space, Spin, Typography, message } from "antd";
import dayjs from "dayjs";
import { useEffect, useMemo, useRef, useState } from "react";
import {
  CastPersonSummary,
  PersonScrapeCandidate,
  applyPersonScrape,
  createPerson,
  fetchPerson,
  resolvePersonAvatarSrc,
  searchPersonScrapeCandidates,
  updatePerson,
  uploadImageFile,
} from "../api/client";
import { proxyImageSrc } from "../lib/imageUrl";
import { useT } from "../i18n";

const OCC_KEYS = [
  "actor", "director", "writer", "producer", "cinematographer",
  "editor", "art_director", "composer", "costume", "other",
] as const;

const AVATAR_PREVIEW_SIZE = 128;

export interface PersonEditModalProps {
  person: CastPersonSummary | null;
  open: boolean;
  onClose: () => void;
  onSaved?: (update: CastPersonSummary) => void;
  /** drawer: side panel (list page create); modal: centered dialog (detail page edit) */
  variant?: "modal" | "drawer";
}

export default function PersonEditModal({
  person,
  open,
  onClose,
  onSaved,
  variant = "modal",
}: PersonEditModalProps) {
  const t = useT();
  const isNew = !person?.id;
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [scraping, setScraping] = useState(false);
  const [candidates, setCandidates] = useState<PersonScrapeCandidate[]>([]);
  const [name, setName] = useState("");
  const [englishName, setEnglishName] = useState("");
  const [gender, setGender] = useState<number>(0);
  const [birthDate, setBirthDate] = useState<string>("");
  const [birthPlace, setBirthPlace] = useState("");
  const [nationality, setNationality] = useState("");
  const [occupations, setOccupations] = useState<string[]>([]);
  const [biography, setBiography] = useState("");
  const [aliases, setAliases] = useState("");
  const [avatar, setAvatar] = useState("");
  const [avatarUploading, setAvatarUploading] = useState(false);
  const avatarFileRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    if (isNew) {
      setName("");
      setEnglishName("");
      setGender(0);
      setBirthDate("");
      setBirthPlace("");
      setNationality("");
      setOccupations([]);
      setBiography("");
      setAliases("");
      setAvatar("");
      setCandidates([]);
      return;
    }
    let cancelled = false;
    setLoading(true);
    void fetchPerson(person!.id)
      .then((detail) => {
        if (cancelled) return;
        setName(detail.name ?? "");
        setEnglishName(detail.english_name ?? "");
        setGender(detail.gender ?? 0);
        setBirthDate(detail.birth_date ?? "");
        setBirthPlace(detail.birth_place ?? "");
        setNationality(detail.nationality ?? "");
        setOccupations(detail.occupations ?? []);
        setBiography(detail.biography ?? "");
        setAliases(detail.aliases ?? "");
        setAvatar(detail.avatar_url ?? "");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, person, isNew]);

  const occOptions = useMemo(
    () => OCC_KEYS.map((k) => ({ value: k, label: t(`occupations.${k}`) })),
    [t],
  );

  async function handleAvatarFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    e.target.value = "";
    if (!file || !(file.type || "").startsWith("image/")) {
      message.warning(t("pages.person_edit.avatar_invalid"));
      return;
    }
    setAvatarUploading(true);
    try {
      const res = await uploadImageFile(file);
      const url = (res.url || res.path || "").trim();
      if (!url) throw new Error("empty upload url");
      setAvatar(url);
      message.success(t("pages.person_edit.avatar_uploaded"));
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.person_edit.avatar_upload_failed"));
    } finally {
      setAvatarUploading(false);
    }
  }

  async function handleScrapeSearch() {
    const q = name.trim() || englishName.trim();
    if (!q) {
      message.warning(t("pages.person_edit.name_required"));
      return;
    }
    setScraping(true);
    try {
      const items = await searchPersonScrapeCandidates(q);
      setCandidates(items);
      if (items.length === 0) message.info(t("pages.person_edit.no_scrape_results"));
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.person_edit.scrape_failed"));
    } finally {
      setScraping(false);
    }
  }

  async function applyCandidate(c: PersonScrapeCandidate) {
    if (isNew) {
      setName(c.name);
      if (c.profile) setAvatar(c.profile);
      if (c.birthday) setBirthDate(c.birthday);
      if (c.gender) setGender(c.gender);
      return;
    }
    setSaving(true);
    try {
      const updated = await applyPersonScrape(person!.id, c.source, c.external_id);
      onSaved?.(updated);
      setName(updated.name ?? c.name);
      setAvatar(updated.avatar_url ?? c.profile ?? "");
      setBirthDate(updated.birth_date ?? c.birthday ?? "");
      message.success(t("pages.person_edit.scrape_applied"));
      setCandidates([]);
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.person_edit.scrape_failed"));
    } finally {
      setSaving(false);
    }
  }

  async function handleSave() {
    if (!name.trim()) {
      message.warning(t("pages.person_edit.name_required"));
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name: name.trim(),
        english_name: englishName.trim(),
        gender,
        birth_date: birthDate,
        birth_place: birthPlace.trim(),
        nationality: nationality.trim(),
        occupations,
        biography: biography.trim(),
        aliases: aliases.trim(),
        avatar_url: avatar.trim(),
      };
      const saved = isNew
        ? await createPerson(payload)
        : await updatePerson(person!.id, payload);
      message.success(t("pages.person_edit.saved"));
      onSaved?.(saved);
      onClose();
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.person_edit.save_failed"));
    } finally {
      setSaving(false);
    }
  }

  const avatarSrc = resolvePersonAvatarSrc(person?.id ?? 0, avatar, person?.updated_at || person?.id);

  const title = isNew ? t("pages.person_edit.create_title") : t("pages.person_edit.edit_title");
  const footer = (
    <Space>
      <Button onClick={onClose}>{t("common.cancel")}</Button>
      <Button type="primary" loading={saving} onClick={() => void handleSave()}>
        {t("common.save")}
      </Button>
    </Space>
  );

  const formBody = loading ? (
    <div style={{ textAlign: "center", padding: 32 }}><Spin /></div>
  ) : (
    <>
          <div style={{ display: "flex", gap: 16, marginBottom: 16 }}>
            <button
              type="button"
              aria-label={t("pages.person_edit.avatar_change_aria")}
              title={t("pages.person_edit.avatar_change_aria")}
              onClick={() => avatarFileRef.current?.click()}
              style={{
                position: "relative",
                flex: `0 0 ${AVATAR_PREVIEW_SIZE}px`,
                width: AVATAR_PREVIEW_SIZE,
                height: AVATAR_PREVIEW_SIZE,
                padding: 0,
                border: "1px solid rgba(255,255,255,0.12)",
                borderRadius: 8,
                overflow: "hidden",
                background: "rgba(255,255,255,0.06)",
                cursor: avatarUploading ? "wait" : "pointer",
              }}
            >
              {avatarUploading ? (
                <span style={{ display: "flex", alignItems: "center", justifyContent: "center", width: "100%", height: "100%" }}>
                  <Spin size="small" />
                </span>
              ) : avatarSrc ? (
                <img src={avatarSrc} alt="" style={{ width: "100%", height: "100%", objectFit: "cover", display: "block" }} />
              ) : (
                <span style={{ display: "flex", alignItems: "center", justifyContent: "center", width: "100%", height: "100%", color: "rgba(255,255,255,0.45)" }}>
                  <CameraOutlined style={{ fontSize: 36 }} />
                </span>
              )}
              {!avatarUploading && (
                <span
                  style={{
                    position: "absolute",
                    inset: 0,
                    display: "flex",
                    alignItems: "center",
                    justifyContent: "center",
                    background: "rgba(0,0,0,0.45)",
                    color: "#fff",
                    fontSize: 12,
                    opacity: 0,
                    transition: "opacity 0.15s ease",
                  }}
                  className="person-edit-avatar-hover"
                >
                  {t("pages.person_edit.avatar_change_hint")}
                </span>
              )}
            </button>
            <input
              ref={avatarFileRef}
              type="file"
              accept="image/jpeg,image/png,image/webp,image/gif"
              style={{ display: "none" }}
              onChange={(e) => void handleAvatarFileChange(e)}
            />
            <div style={{ flex: 1 }}>
              <Input placeholder={t("pages.person_edit.name")} value={name} onChange={(e) => setName(e.target.value)} style={{ marginBottom: 8 }} />
              <Input placeholder={t("pages.person_edit.english_name")} value={englishName} onChange={(e) => setEnglishName(e.target.value)} style={{ marginBottom: 8 }} />
              <Button icon={<CloudDownloadOutlined />} loading={scraping} onClick={() => void handleScrapeSearch()}>
                {t("pages.person_edit.scrape_from_web")}
              </Button>
            </div>
          </div>

          {candidates.length > 0 && (
            <div style={{ marginBottom: 16 }}>
              <Typography.Text type="secondary">{t("pages.person_edit.pick_candidate")}</Typography.Text>
              <div style={{ display: "flex", gap: 8, overflowX: "auto", marginTop: 8 }}>
                {candidates.map((c) => (
                  <div key={c.external_id} style={{ flex: "0 0 120px", textAlign: "center", cursor: "pointer" }} onClick={() => void applyCandidate(c)}>
                    {c.profile && <img src={proxyImageSrc(c.profile) || c.profile} alt="" style={{ width: 72, height: 72, borderRadius: "50%", objectFit: "cover" }} />}
                    <div style={{ fontSize: 12 }}>{c.name}</div>
                    {c.known_for && <div style={{ fontSize: 11, opacity: 0.6 }}>{c.known_for}</div>}
                  </div>
                ))}
              </div>
            </div>
          )}

          <Input placeholder={t("pages.person_edit.aliases")} value={aliases} onChange={(e) => setAliases(e.target.value)} style={{ marginBottom: 8 }} />
          <Select
            style={{ width: "100%", marginBottom: 8 }}
            value={gender}
            onChange={setGender}
            options={[
              { value: 0, label: t("pages.person_edit.gender_unknown") },
              { value: 1, label: t("pages.person_edit.gender_male") },
              { value: 2, label: t("pages.person_edit.gender_female") },
            ]}
          />
          <DatePicker
            style={{ width: "100%", marginBottom: 8 }}
            value={birthDate ? dayjs(birthDate) : null}
            onChange={(d) => setBirthDate(d ? d.format("YYYY-MM-DD") : "")}
            placeholder={t("pages.person_edit.birth_date")}
          />
          <Input placeholder={t("pages.person_edit.birth_place")} value={birthPlace} onChange={(e) => setBirthPlace(e.target.value)} style={{ marginBottom: 8 }} />
          <Input placeholder={t("pages.person_edit.nationality")} value={nationality} onChange={(e) => setNationality(e.target.value)} style={{ marginBottom: 8 }} />
          <Select mode="multiple" style={{ width: "100%", marginBottom: 8 }} placeholder={t("pages.person_edit.occupations")} value={occupations} onChange={setOccupations} options={occOptions} />
          <Input.TextArea rows={4} placeholder={t("pages.person_edit.biography")} value={biography} onChange={(e) => setBiography(e.target.value)} />
    </>
  );

  if (variant === "drawer") {
    return (
      <>
        <style>{`
          button:hover .person-edit-avatar-hover { opacity: 1 !important; }
          button:focus-visible .person-edit-avatar-hover { opacity: 1 !important; }
        `}</style>
        <Drawer
        title={title}
        open={open}
        onClose={onClose}
        width={720}
        destroyOnClose
        extra={footer}
      >
        {formBody}
        </Drawer>
      </>
    );
  }

  return (
    <>
      <style>{`
        button:hover .person-edit-avatar-hover { opacity: 1 !important; }
        button:focus-visible .person-edit-avatar-hover { opacity: 1 !important; }
      `}</style>
      <Modal open={open} title={title} onCancel={onClose} width={720} footer={footer}>
        {formBody}
      </Modal>
    </>
  );
}
