import type { DocumentFacet, DocumentItem } from "../api/client";

export type DocumentTagUpdateResult = {
  items: DocumentItem[];
  facets: DocumentFacet[];
};

const normalizedTag = (tag: string) => tag.trim().toLocaleLowerCase();

export function applyDocumentTagUpdates(
  items: DocumentItem[],
  facets: DocumentFacet[],
  updates: ReadonlyMap<number, string[]>,
  activeTagFilter?: string,
  facetDeltas: ReadonlyArray<{ tag: string; delta: number }> = [],
  applyFacetDeltas = true,
): DocumentTagUpdateResult {
  const facetMap = new Map<string, DocumentFacet>();
  for (const facet of facets) facetMap.set(normalizedTag(facet.name), { ...facet });

  const nextItems = items.map((item) => {
    const nextTags = updates.get(item.id);
    if (!nextTags) return item;
    return { ...item, tags: nextTags };
  });

  for (const delta of applyFacetDeltas ? facetDeltas : []) {
    const key = normalizedTag(delta.tag);
    const facet = facetMap.get(key);
    if (facet) facet.count = Math.max(0, facet.count + delta.delta);
    else if (delta.delta > 0) facetMap.set(key, { name: delta.tag, count: delta.delta });
  }

  const filterKey = activeTagFilter ? normalizedTag(activeTagFilter) : "";
  return {
    items: filterKey ? nextItems.filter((item) => (item.tags ?? []).some((tag) => normalizedTag(tag) === filterKey)) : nextItems,
    facets: [...facetMap.values()].filter((facet) => facet.count > 0).sort((a, b) => b.count - a.count || a.name.localeCompare(b.name)),
  };
}
