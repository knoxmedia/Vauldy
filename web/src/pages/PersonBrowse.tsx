import { PlusOutlined, SearchOutlined, TeamOutlined, UserOutlined } from "@ant-design/icons";
import { Button, Empty, Input, Pagination, Select, Spin, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  CastPersonSummary,
  fetchPersons,
  resolvePersonAvatarSrc,
} from "../api/client";
import PersonEditModal from "../components/PersonEditModal";
import { isAdminRole, useAuthStore } from "../store/auth";
import { useT } from "../i18n";
import styles from "./PersonBrowse.module.css";

const OCC_KEYS = [
  "actor",
  "director",
  "writer",
  "producer",
  "cinematographer",
  "editor",
  "art_director",
  "composer",
  "costume",
  "other",
] as const;

export default function PersonBrowsePage() {
  const nav = useNavigate();
  const t = useT();
  const admin = isAdminRole(useAuthStore((s) => s.role));
  const [items, setItems] = useState<CastPersonSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState("");
  const [occupation, setOccupation] = useState<string>("");
  const [scraped, setScraped] = useState<string>("");
  const [sort, setSort] = useState<string>("name");
  const [createOpen, setCreateOpen] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [brokenAvatars, setBrokenAvatars] = useState<Record<number, true>>({});

  useEffect(() => {
    setBrokenAvatars({});
  }, [items, refreshKey]);
  const pageSize = 48;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    void fetchPersons({
      q: search.trim() || undefined,
      occupation: occupation || undefined,
      scraped: scraped || undefined,
      sort,
      page,
      page_size: pageSize,
    })
      .then((data) => {
        if (cancelled) return;
        setItems(data.items ?? []);
        setTotal(data.total ?? 0);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [search, occupation, scraped, sort, page, refreshKey]);

  const occOptions = useMemo(
    () => [
      { value: "", label: t("pages.person_browse.filter_all_occupations") },
      ...OCC_KEYS.map((k) => ({ value: k, label: t(`occupations.${k}`) })),
    ],
    [t],
  );

  return (
    <div className={styles.wrap}>
      <div className={styles.header}>
        <div>
          <Typography.Title level={3} className={styles.title}>
            <TeamOutlined /> {t("pages.person_browse.title")}
          </Typography.Title>
          <Typography.Text type="secondary">{t("pages.person_browse.subtitle", { total })}</Typography.Text>
        </div>
        {admin && (
          <Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
            {t("pages.person_browse.add_person")}
          </Button>
        )}
      </div>

      <div className={styles.filters}>
        <Input
          allowClear
          prefix={<SearchOutlined />}
          placeholder={t("pages.person_browse.search_placeholder")}
          value={search}
          onChange={(e) => {
            setPage(1);
            setSearch(e.target.value);
          }}
          className={styles.searchInput}
        />
        <Select
          value={occupation}
          options={occOptions}
          onChange={(v) => {
            setPage(1);
            setOccupation(v);
          }}
          className={styles.filterSelect}
        />
        <Select
          value={scraped}
          options={[
            { value: "", label: t("pages.person_browse.filter_all_scrape") },
            { value: "yes", label: t("pages.person_browse.filter_scraped") },
            { value: "no", label: t("pages.person_browse.filter_unscraped") },
          ]}
          onChange={(v) => {
            setPage(1);
            setScraped(v);
          }}
          className={styles.filterSelect}
        />
        <Select
          value={sort}
          options={[
            { value: "name", label: t("pages.person_browse.sort_name") },
            { value: "works", label: t("pages.person_browse.sort_works") },
            { value: "created", label: t("pages.person_browse.sort_created") },
          ]}
          onChange={setSort}
          className={styles.filterSelect}
        />
      </div>

      {loading ? (
        <div className={styles.center}><Spin size="large" /></div>
      ) : items.length === 0 ? (
        <Empty description={t("pages.person_browse.empty")} />
      ) : (
        <>
          <div className={styles.grid}>
            {items.map((p) => {
              const avatar = resolvePersonAvatarSrc(p.id, p.avatar_url, p.updated_at || p.id);
              const showAvatar = !!avatar && !brokenAvatars[p.id];
              return (
                <div key={p.id} className={styles.card} onClick={() => nav(`/person/${p.id}`)} role="button" tabIndex={0}>
                  <div className={styles.avatarWrap}>
                    {showAvatar ? (
                      <img
                        src={avatar}
                        alt=""
                        className={styles.avatar}
                        loading="lazy"
                        onError={() => setBrokenAvatars((prev) => ({ ...prev, [p.id]: true }))}
                      />
                    ) : (
                      <div className={styles.avatarEmpty} aria-hidden />
                    )}
                  </div>
                  <div className={styles.name}>{p.name}</div>
                  <div className={styles.meta}>
                    {(p.occupations ?? []).slice(0, 2).map((o) => t(`occupations.${o}`)).join(" · ") ||
                      t("pages.person_browse.no_occupation")}
                  </div>
                  <div className={styles.works}>
                    <UserOutlined /> {t("pages.person_browse.work_count", { n: p.work_count ?? 0 })}
                  </div>
                </div>
              );
            })}
          </div>
          {total > pageSize && (
            <div className={styles.pagination}>
              <Pagination current={page} pageSize={pageSize} total={total} onChange={setPage} showSizeChanger={false} />
            </div>
          )}
        </>
      )}

      <PersonEditModal
        person={null}
        open={createOpen}
        variant="drawer"
        onClose={() => setCreateOpen(false)}
        onSaved={() => {
          setCreateOpen(false);
          setRefreshKey((k) => k + 1);
        }}
      />
    </div>
  );
}
