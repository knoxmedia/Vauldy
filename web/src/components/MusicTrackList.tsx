import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  CaretRightOutlined,
  CheckOutlined,
  EditOutlined,
  EllipsisOutlined,
  SlidersOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Checkbox, Dropdown, Popover } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { MusicTrackRow } from "../api/client";
import {
  type TrackColumnDef,
  type TrackColumnId,
  type TrackSortOrder,
  availableColumns,
  compareTracks,
  fmtBitrate,
  fmtDate,
  fmtDuration,
  readVisibleColumns,
  storeVisibleColumns,
} from "../lib/musicTrackColumns";
import browseStyles from "../pages/Browse.module.css";
import musicStyles from "../pages/MusicBrowse.module.css";
import { currentMusicTrack, useMusicPlayerStore } from "../store/musicPlayer";
import { useT } from "../i18n";
import NowPlayingIndicator from "./NowPlayingIndicator";

type Props = {
  tracks: MusicTrackRow[];
  onPlayTrack: (mediaId: number, orderedTracks: MusicTrackRow[]) => void;
  resolveArtistId: (track: MusicTrackRow) => number | null;
  buildTrackMenu: (track: MusicTrackRow) => MenuProps;
  showAlbumColumn?: boolean;
  showHeader?: boolean;
};

function ColumnPicker({
  columns,
  visible,
  onChange,
}: {
  columns: TrackColumnDef[];
  visible: Set<TrackColumnId>;
  onChange: (next: Set<TrackColumnId>) => void;
}) {
  const t = useT();
  const [open, setOpen] = useState(false);

  function toggle(id: TrackColumnId) {
    if (id === "title") return;
    const next = new Set(visible);
    if (next.has(id)) {
      if (next.size <= 1) return;
      next.delete(id);
    } else {
      next.add(id);
    }
    onChange(next);
    storeVisibleColumns(next);
  }

  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      trigger="click"
      placement="bottomLeft"
      classNames={{ root: browseStyles.browseColPickerOverlay }}
      content={
        <div className={browseStyles.browseColPickerList}>
          {columns.map((col) => {
            const checked = visible.has(col.id);
            return (
              <label key={col.id} className={browseStyles.browseColPickerRow}>
                <Checkbox
                  checked={checked}
                  disabled={col.id === "title"}
                  onChange={() => toggle(col.id)}
                />
                <span className={checked ? browseStyles.browseColPickerActive : browseStyles.browseColPickerMuted}>
                  {col.label}
                </span>
              </label>
            );
          })}
        </div>
      }
    >
      <Button
        type="text"
        size="small"
        icon={<SlidersOutlined />}
        aria-label={t("components.music_track_list.col_aria")}
        className={browseStyles.browseColPickerTrigger}
        onClick={(e) => e.stopPropagation()}
      />
    </Popover>
  );
}

function SortableHeader({
  label,
  active,
  order,
  onClick,
}: {
  label: string;
  active: boolean;
  order: TrackSortOrder;
  onClick: () => void;
}) {
  return (
    <button type="button" className={musicStyles.sortHeaderBtn} onClick={onClick}>
      <span>{label}</span>
      {active ? (
        order === "asc" ? (
          <ArrowUpOutlined className={musicStyles.sortHeaderIcon} />
        ) : (
          <ArrowDownOutlined className={musicStyles.sortHeaderIcon} />
        )
      ) : null}
    </button>
  );
}

export default function MusicTrackList({
  tracks,
  onPlayTrack,
  resolveArtistId,
  buildTrackMenu,
  showAlbumColumn = true,
  showHeader = true,
}: Props) {
  const t = useT();
  const nav = useNavigate();
  const playerPlaying = useMusicPlayerStore((s) => s.playing);
  const currentMediaId = useMusicPlayerStore((s) => currentMusicTrack(s)?.mediaId ?? null);

  const columnDefs = useMemo(() => availableColumns(showAlbumColumn), [showAlbumColumn]);
  const [visibleColumns, setVisibleColumns] = useState<Set<TrackColumnId>>(() => readVisibleColumns(showAlbumColumn));
  const [sortField, setSortField] = useState<TrackColumnId>("title");
  const [sortOrder, setSortOrder] = useState<TrackSortOrder>("asc");
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set());

  function toggleSelect(mediaId: number) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(mediaId)) next.delete(mediaId);
      else next.add(mediaId);
      return next;
    });
  }

  useEffect(() => {
    setVisibleColumns(readVisibleColumns(showAlbumColumn));
  }, [showAlbumColumn]);

  const activeColumns = useMemo(
    () => columnDefs.filter((c) => visibleColumns.has(c.id)),
    [columnDefs, visibleColumns],
  );

  const sortedTracks = useMemo(() => {
    const col = columnDefs.find((c) => c.id === sortField);
    if (!col?.sortable) return tracks;
    return [...tracks].sort((a, b) => compareTracks(a, b, sortField, sortOrder));
  }, [tracks, sortField, sortOrder, columnDefs]);

  function toggleSort(field: TrackColumnId) {
    const col = columnDefs.find((c) => c.id === field);
    if (!col?.sortable) return;
    if (sortField === field) {
      setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
    } else {
      setSortField(field);
      setSortOrder("asc");
    }
  }

  function renderCell(track: MusicTrackRow, col: TrackColumnDef) {
    switch (col.id) {
      case "title":
        return <span className={musicStyles.trackTitle}>{track.title}</span>;
      case "album_artist": {
        const name = track.album_artist || "—";
        const id = track.artist_id && track.artist_id > 0 ? track.artist_id : resolveArtistId(track);
        return id ? (
          <button
            type="button"
            className={musicStyles.trackLink}
            onClick={(e) => {
              e.stopPropagation();
              nav(`/artist/${id}`);
            }}
          >
            {name}
          </button>
        ) : (
          <span className={musicStyles.trackMuted}>{name}</span>
        );
      }
      case "artist": {
        const name = track.artist || "—";
        const id = resolveArtistId(track);
        return id ? (
          <button
            type="button"
            className={musicStyles.trackLink}
            onClick={(e) => {
              e.stopPropagation();
              nav(`/artist/${id}`);
            }}
          >
            {name}
          </button>
        ) : (
          <span className={musicStyles.trackMuted}>{name}</span>
        );
      }
      case "album":
        return track.album_id ? (
          <button
            type="button"
            className={musicStyles.trackLink}
            onClick={(e) => {
              e.stopPropagation();
              nav(`/album/${track.album_id}`);
            }}
          >
            {track.album_title || "—"}
          </button>
        ) : (
          <span className={musicStyles.trackMuted}>{track.album_title || "—"}</span>
        );
      case "duration":
        return fmtDuration(track.duration);
      case "bitrate":
        return fmtBitrate(track.bitrate);
      case "added_at":
        return fmtDate(track.created_at);
      case "rating":
      case "plays":
      case "played_at":
      case "rated_at":
      case "popularity":
        return <span className={musicStyles.trackMuted}>—</span>;
      default:
        return "—";
    }
  }

  return (
    <div className={musicStyles.tableWrap}>
      <table className={musicStyles.table}>
        {showHeader ? (
          <thead>
            <tr>
              <th className={musicStyles.columnPickerCell}>
                <ColumnPicker columns={columnDefs} visible={visibleColumns} onChange={setVisibleColumns} />
              </th>
              <th style={{ width: 48 }}>#</th>
              {activeColumns.map((col) => (
                <th key={col.id}>
                  {col.sortable ? (
                    <SortableHeader
                      label={col.label}
                      active={sortField === col.id}
                      order={sortOrder}
                      onClick={() => toggleSort(col.id)}
                    />
                  ) : (
                    col.label
                  )}
                </th>
              ))}
              <th style={{ width: 72 }} aria-label={t("components.music_track_list.actions_aria")} />
            </tr>
          </thead>
        ) : null}
        <tbody>
          {sortedTracks.map((tr, idx) => {
            const isSelected = selectedIds.has(tr.media_id);
            return (
            <tr
              key={tr.id}
              className={musicStyles.trackRow}
              data-selected={isSelected ? "" : undefined}
            >
              <td className={musicStyles.columnPickerCell}>
                <button
                  type="button"
                  className={musicStyles.trackGutterSelect}
                  aria-label={isSelected ? t("components.music_track_list.aria_deselect") : t("components.music_track_list.aria_select")}
                  aria-pressed={isSelected}
                  data-selected={isSelected ? "" : undefined}
                  onClick={(e) => {
                    e.stopPropagation();
                    toggleSelect(tr.media_id);
                  }}
                >
                  {isSelected ? <CheckOutlined /> : null}
                </button>
              </td>
              <td className={musicStyles.trackIndexCell}>
                {currentMediaId === tr.media_id ? (
                  <NowPlayingIndicator playing={playerPlaying} />
                ) : (
                  <>
                    <span className={musicStyles.trackIndexNum}>{tr.track_number || idx + 1}</span>
                    <button
                      type="button"
                      className={musicStyles.trackPlayBtn}
                      aria-label={t("components.music_track_list.aria_play_track", { title: tr.title })}
                      onClick={(e) => {
                        e.stopPropagation();
                        onPlayTrack(tr.media_id, sortedTracks);
                      }}
                    >
                      <CaretRightOutlined />
                    </button>
                  </>
                )}
              </td>
              {activeColumns.map((col) => (
                <td key={col.id}>{renderCell(tr, col)}</td>
              ))}
              <td className={musicStyles.rowActionsCell}>
                <div className={musicStyles.rowActions}>
                  <Button
                    type="text"
                    size="small"
                    className={musicStyles.rowActionBtn}
                    icon={<EditOutlined />}
                    aria-label={t("components.music_track_list.aria_edit")}
                    onClick={(e) => {
                      e.stopPropagation();
                      nav(`/detail/${tr.media_id}`);
                    }}
                  />
                  <Dropdown menu={buildTrackMenu(tr)} trigger={["click"]} placement="bottomRight">
                    <Button
                      type="text"
                      size="small"
                      className={musicStyles.rowActionBtn}
                      icon={<EllipsisOutlined rotate={90} />}
                      aria-label={t("components.music_track_list.aria_more")}
                      onClick={(e) => e.stopPropagation()}
                    />
                  </Dropdown>
                </div>
              </td>
            </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
