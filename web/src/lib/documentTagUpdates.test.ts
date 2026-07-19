import { describe, expect, it } from "vitest";
import type { DocumentFacet, DocumentItem } from "../api/client";
import { applyDocumentTagUpdates } from "./documentTagUpdates";

const items: DocumentItem[] = [
  { id: 1, title: "One", format: "pdf", tags: ["Alpha", "Shared"] },
  { id: 2, title: "Two", format: "pdf", tags: ["Beta", "Shared"] },
];
const facets: DocumentFacet[] = [
  { name: "Alpha", count: 5 },
  { name: "Beta", count: 3 },
  { name: "Shared", count: 10 },
  { name: "Unseen", count: 7 },
];

describe("applyDocumentTagUpdates", () => {
  it("applies normalized facet deltas while preserving unseen counts", () => {
    const result = applyDocumentTagUpdates(items, facets, new Map([[1, ["ALPHA", "Gamma"]]]), undefined, [{ tag: "shared", delta: -2 }, { tag: "gamma", delta: 2 }]);
    expect(result.items[0]?.tags).toEqual(["ALPHA", "Gamma"]);
    expect(result.facets).toEqual(expect.arrayContaining([
      { name: "Alpha", count: 5 },
      { name: "Beta", count: 3 },
      { name: "Shared", count: 8 },
      { name: "gamma", count: 2 },
      { name: "Unseen", count: 7 },
    ]));
  });

  it("removes rows that no longer match the active tag filter", () => {
    const filteredItems = items.map((item) => ({ ...item, tags: [...(item.tags ?? []), "Alpha"] }));
    const result = applyDocumentTagUpdates(filteredItems, facets, new Map([[1, ["Gamma"]]]), "alpha");
    expect(result.items.map((item) => item.id)).toEqual([2]);
  });

  it("keeps all rows when no tag filter is active", () => {
    const result = applyDocumentTagUpdates(items, facets, new Map([[1, ["Gamma"]]]));
    expect(result.items.map((item) => item.id)).toEqual([1, 2]);
  });
});


it("does not apply tag deltas to a non-tag facet dataset", () => {
  const authorFacets: DocumentFacet[] = [{ name: "alpha", count: 4 }, { name: "Writer", count: 2 }];
  const result = applyDocumentTagUpdates(items, authorFacets, new Map([[1, ["Gamma"]]]), undefined, [{ tag: "alpha", delta: -3 }], false);
  expect(result.facets).toEqual(authorFacets);
  expect(result.items[0]?.tags).toEqual(["Gamma"]);
});
