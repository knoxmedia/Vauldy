import {
  ArrowLeftOutlined,
  EditOutlined,
  TeamOutlined,
} from "@ant-design/icons";
import { Button, Empty, Spin, Tabs, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  CastPersonSummary,
  MediaPersonLink,
  PersonCollaborator,
  fetchPerson,
  fetchPersonCollaborators,
  fetchPersonWorks,
  localPosterSrc,
  normalizeListPosterUrl,
  resolvePersonAvatarSrc,
} from "../api/client";
import PersonEditModal from "../components/PersonEditModal";
import { isAdminRole, useAuthStore } from "../store/auth";
import { useT } from "../i18n";
import styles from "./PersonDetail.module.css";

const OCC_ORDER = ["actor", "director", "writer", "producer", "cinematographer", "editor", "art_director", "composer", "costume", "other"];

function roleLabel(link: MediaPersonLink, t: ReturnType<typeof useT>): string {
  if (link.occupation === "actor" && link.character_name) {
    return t("pages.person_detail.as_character", { name: link.character_name });
  }
  return t(`occupations.${link.occupation}`);
}

export default function PersonDetailPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const t = useT();
  const admin = isAdminRole(useAuthStore((s) => s.role));
  const personId = Number(id);
  const [person, setPerson] = useState<CastPersonSummary | null>(null);
  const [works, setWorks] = useState<MediaPersonLink[]>([]);
  const [collabs, setCollabs] = useState<PersonCollaborator[]>([]);
  const [loading, setLoading] = useState(true);
  const [activeOcc, setActiveOcc] = useState<string>("actor");
  const [editOpen, setEditOpen] = useState(false);
  const [brokenAvatars, setBrokenAvatars] = useState<Record<number, true>>({});

  useEffect(() => {
    setBrokenAvatars({});
  }, [personId, person?.avatar_url, person?.updated_at]);

  useEffect(() => {
    if (!Number.isFinite(personId) || personId <= 0) {
      nav("/persons", { replace: true });
      return;
    }
    let cancelled = false;
    setLoading(true);
    void Promise.all([fetchPerson(personId), fetchPersonCollaborators(personId)])
      .then(([p, c]) => {
        if (cancelled) return;
        setPerson(p);
        setCollabs(c);
        const counts = p.occupation_counts ?? {};
        const top = OCC_ORDER.find((o) => (counts[o] ?? 0) > 0) ?? "actor";
        setActiveOcc(top);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [personId, nav]);

  useEffect(() => {
    if (!Number.isFinite(personId) || personId <= 0) return;
    let cancelled = false;
    void fetchPersonWorks(personId, activeOcc).then((items) => {
      if (!cancelled) setWorks(items);
    });
    return () => {
      cancelled = true;
    };
  }, [personId, activeOcc]);

  const occTabs = useMemo(() => {
    const counts = person?.occupation_counts ?? {};
    return OCC_ORDER.filter((o) => (counts[o] ?? 0) > 0).map((o) => ({
      key: o,
      label: `${t(`occupations.${o}`)}(${counts[o] ?? 0})`,
    }));
  }, [person, t]);

  if (loading || !person) {
    return <div className={styles.center}><Spin size="large" /></div>;
  }

  const avatar = resolvePersonAvatarSrc(person.id, person.avatar_url, person.updated_at || person.id);
  const showHeroAvatar = !!avatar && !brokenAvatars[person.id];

  return (
    <div className={styles.wrap}>
      <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)} className={styles.back}>
        {t("common.back")}
      </Button>

      <div className={styles.hero}>
        <div className={styles.avatarWrap}>
          {showHeroAvatar ? (
            <img
              src={avatar}
              alt=""
              className={styles.avatar}
              onError={() => setBrokenAvatars((prev) => ({ ...prev, [person.id]: true }))}
            />
          ) : (
            <div className={styles.avatarEmpty} aria-hidden />
          )}
        </div>
        <div className={styles.info}>
          {admin && (
            <Button icon={<EditOutlined />} onClick={() => setEditOpen(true)} className={styles.editBtn}>
              {t("common.edit")}
            </Button>
          )}
          <Typography.Title level={2} className={styles.name}>{person.name}</Typography.Title>
          {person.english_name && <div className={styles.enName}>{person.english_name}</div>}
          <div className={styles.tags}>
            {(person.occupations ?? []).map((o) => (
              <span key={o} className={styles.tag}>{t(`occupations.${o}`)}</span>
            ))}
          </div>
          <div className={styles.metaLine}>
            {[person.birth_date, person.birth_place, person.nationality].filter(Boolean).join(" · ")}
          </div>
        </div>
      </div>

      {person.biography && (
        <section className={styles.section}>
          <Typography.Title level={4}>{t("pages.person_detail.biography")}</Typography.Title>
          <Typography.Paragraph className={styles.bio}>{person.biography}</Typography.Paragraph>
        </section>
      )}

      {collabs.length > 0 && (
        <section className={styles.section}>
          <Typography.Title level={4}><TeamOutlined /> {t("pages.person_detail.collaborators")}</Typography.Title>
          <div className={styles.collabRow}>
            {collabs.map((c) => {
              const collabAvatar = resolvePersonAvatarSrc(c.person_id, c.avatar_url, c.person_id);
              const showCollabAvatar = !!collabAvatar && !brokenAvatars[c.person_id];
              return (
              <div key={c.person_id} className={styles.collabCard} onClick={() => nav(`/person/${c.person_id}`)} role="button" tabIndex={0}>
                {showCollabAvatar ? (
                  <img
                    src={collabAvatar}
                    alt=""
                    className={styles.collabAvatar}
                    onError={() => setBrokenAvatars((prev) => ({ ...prev, [c.person_id]: true }))}
                  />
                ) : (
                  <div className={styles.collabAvatarEmpty} aria-hidden />
                )}
                <div className={styles.collabName}>{c.name}</div>
                <div className={styles.collabCount}>{t("pages.person_detail.collab_count", { n: c.collaboration_count })}</div>
              </div>
              );
            })}
          </div>
        </section>
      )}

      <section className={styles.section}>
        <Typography.Title level={4}>{t("pages.person_detail.filmography")}</Typography.Title>
        {occTabs.length === 0 ? (
          <Empty description={t("pages.person_detail.no_works")} />
        ) : (
          <>
            <Tabs activeKey={activeOcc} items={occTabs} onChange={setActiveOcc} className={styles.tabs} />
            {works.length === 0 ? (
              <Empty description={t("pages.person_detail.no_works")} />
            ) : (
            <div className={styles.workGrid}>
              {works.map((w) => {
                const poster =
                  normalizeListPosterUrl(w.poster_url || "") || localPosterSrc(w.media_id);
                const title = (w.media_title || "").trim() || t("pages.media_detail.untitled");
                return (
                <div key={w.id} className={styles.workCard} onClick={() => nav(`/detail/${w.media_id}`)} role="button" tabIndex={0}>
                  <img src={poster} alt="" className={styles.poster} loading="lazy" />
                  <div className={styles.workTitle}>{title}</div>
                  <div className={styles.workMeta}>
                    {w.media_year ? `${w.media_year} · ` : ""}{roleLabel(w, t)}
                  </div>
                </div>
                );
              })}
            </div>
            )}
          </>
        )}
      </section>

      <PersonEditModal
        person={person}
        open={editOpen}
        onClose={() => setEditOpen(false)}
        onSaved={(p) => setPerson((prev) => ({ ...prev!, ...p }))}
      />
    </div>
  );
}
