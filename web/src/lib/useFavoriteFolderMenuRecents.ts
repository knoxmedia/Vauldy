import { useCallback, useEffect, useState } from "react";
import { fetchFavoriteFolders } from "../api/client";
import {
  mergeRecentFavoriteFolders,
  readRecentFavoriteFolders,
  rememberFavoriteFolderAdded,
  type RecentFavoriteFolderEntry,
} from "./recentFavoriteFolders";

export function useFavoriteFolderMenuRecents() {
  const [recentFavoriteFolders, setRecentFavoriteFolders] = useState<RecentFavoriteFolderEntry[]>(
    () => readRecentFavoriteFolders(),
  );

  const reloadRecentFavoriteFolders = useCallback(async () => {
    try {
      const folders = await fetchFavoriteFolders();
      setRecentFavoriteFolders(mergeRecentFavoriteFolders(folders));
    } catch {
      setRecentFavoriteFolders(readRecentFavoriteFolders());
    }
  }, []);

  useEffect(() => {
    void reloadRecentFavoriteFolders();
  }, [reloadRecentFavoriteFolders]);

  const rememberFolderMenuAdded = useCallback(
    (entry: RecentFavoriteFolderEntry) => {
      rememberFavoriteFolderAdded(entry);
      void reloadRecentFavoriteFolders();
    },
    [reloadRecentFavoriteFolders],
  );

  return { recentFavoriteFolders, rememberFolderMenuAdded, reloadRecentFavoriteFolders };
}
