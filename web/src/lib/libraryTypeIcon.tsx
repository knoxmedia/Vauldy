import type { ReactNode } from "react";
import {
  CustomerServiceOutlined,
  FileTextOutlined,
  FolderOpenOutlined,
  PictureOutlined,
  VideoCameraOutlined,
} from "@ant-design/icons";
import { AnimeLibraryIcon, MovieLibraryIcon, TvSeriesLibraryIcon } from "./libraryMediaIcons";

/** Sidebar / nav icon for a media library by its `library.type` value. */
export function libraryTypeIcon(type?: string): ReactNode {
  switch ((type || "").trim().toLowerCase()) {
    case "movie":
      return <MovieLibraryIcon />;
    case "tv":
    case "television":
    case "series":
      return <TvSeriesLibraryIcon />;
    case "anime":
      return <AnimeLibraryIcon />;
    case "video":
      return <VideoCameraOutlined />;
    case "music":
      return <CustomerServiceOutlined />;
    case "photo":
      return <PictureOutlined />;
    case "document":
      return <FileTextOutlined />;
    default:
      return <FolderOpenOutlined />;
  }
}
