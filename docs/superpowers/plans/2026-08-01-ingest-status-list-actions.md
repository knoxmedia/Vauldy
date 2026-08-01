# Ingest Status List Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Media Manager「入库状态」scrollable, show failure/degraded reasons, and support retry + confirmed delete for those rows.

**Architecture:** Frontend-only. Reuse `publication_error` from admin media list, `retryAdminMediaIngest`, and `deleteMedia` (with the same deletion-plan confirm pattern as `mediaMenuItems`). No new backend.

**Tech Stack:** React, Ant Design List/Card/Popconfirm or Modal, Vitest + Testing Library, existing `web/src/api/client.ts`.

**Spec:** `docs/superpowers/specs/2026-08-01-ingest-status-list-actions-design.md`

---

## File map

| File | Responsibility |
|------|----------------|
| `web/src/api/client.ts` | Ensure `AdminMediaItem` includes `publication_error?: string` |
| `web/src/pages/MediaManager.tsx` | Scrollable list UI, reason text, retry/remove actions |
| `web/src/i18n/locales/zh-CN.json` | New `pages.media_manager.*` strings |
| `web/src/i18n/locales/en.json` | Same keys (English) |
| `web/src/i18n/locales/zh-TW.json` | Same keys (Traditional) |
| `web/src/pages/__tests__/MediaManager.publication.test.tsx` | Tests for reason + retry + remove + no actions on processing |

---

### Task 1: Types + i18n keys

**Files:**
- Modify: `media/web/src/api/client.ts` (`AdminMediaItem`)
- Modify: `media/web/src/i18n/locales/zh-CN.json`
- Modify: `media/web/src/i18n/locales/en.json`
- Modify: `media/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: Extend `AdminMediaItem`**

In `client.ts`, change:

```ts
export type AdminMediaItem = MediaItem & {
  publication_state: PublicationState;
  ingest_generation: number;
};
```

to:

```ts
export type AdminMediaItem = MediaItem & {
  publication_state: PublicationState;
  publication_error?: string;
  ingest_generation: number;
};
```

(`MediaItem` may already have optional `publication_error`; keep the field on `AdminMediaItem` explicitly for list typing.)

- [ ] **Step 2: Add i18n keys** under `pages.media_manager` in all three locale files (place near existing `ingest_*` / `publication_*` keys):

| Key | zh-CN | en | zh-TW |
|-----|-------|----|-------|
| `ingest_no_reason` | 暂无失败原因 | No failure reason provided | 暫無失敗原因 |
| `ingest_retry` | 重试 | Retry | 重試 |
| `ingest_remove` | 移除 | Remove | 移除 |
| `ingest_remove_confirm_title` | 删除媒体？ | Delete media? | 刪除媒體？ |
| `ingest_remove_warning` | 将从媒体库删除该条目，并删除关联文件（如有）。此操作不可恢复。 | This removes the item from the library and deletes related files if present. This cannot be undone. | 將從媒體庫刪除此項目，並刪除關聯檔案（如有）。此操作無法復原。 |
| `ingest_remove_confirm_ok` | 确认删除 | Delete | 確認刪除 |
| `ingest_retry_success` | 已提交重新入库 | Re-ingest submitted | 已提交重新入庫 |
| `ingest_retry_failed` | 重试失败 | Retry failed | 重試失敗 |
| `ingest_remove_success` | 已删除媒体 | Media deleted | 已刪除媒體 |
| `ingest_remove_failed` | 删除失败 | Delete failed | 刪除失敗 |

- [ ] **Step 3: Commit** (only if the user asked to commit; otherwise skip)

```bash
git add web/src/api/client.ts web/src/i18n/locales/zh-CN.json web/src/i18n/locales/en.json web/src/i18n/locales/zh-TW.json
git commit -m "chore: i18n and types for ingest status actions"
```

---

### Task 2: Failing tests for ingest list actions

**Files:**
- Modify: `media/web/src/pages/__tests__/MediaManager.publication.test.tsx`

- [ ] **Step 1: Extend mocks and helpers**

Add to `vi.hoisted` mocks:

```ts
retryAdminMediaIngest: vi.fn(),
deleteMedia: vi.fn(),
fetchMediaDeletionPlan: vi.fn(),
messageSuccess: vi.fn(),
```

Wire them in the `../../api/client` mock and antd `message.success`.

Update `adminMedia` helper to accept optional `publication_error`:

```ts
function adminMedia(
  id: number,
  state: AdminMediaItem["publication_state"],
  title: string = state,
  libraryId = 1,
  publication_error = "",
): AdminMediaItem {
  return {
    id, library_id: libraryId, file_id: `f${id}`, title, file_path: `${title}.mkv`, file_type: "video",
    duration: 0, width: 0, height: 0, format: "", status: "active",
    publication_state: state, publication_error, ingest_generation: 1,
  };
}
```

In `beforeEach`, set:

```ts
mocks.retryAdminMediaIngest.mockResolvedValue({});
mocks.deleteMedia.mockResolvedValue(undefined);
mocks.fetchMediaDeletionPlan.mockResolvedValue(["/tmp/a.mkv"]);
```

- [ ] **Step 2: Add tests** (append to the describe block)

```ts
it("shows publication_error for failed and degraded rows", async () => {
  mocks.fetchAdminMedia.mockResolvedValue({
    items: [
      adminMedia(1, "failed", "Fail Film", 1, "prepare exhausted"),
      adminMedia(2, "degraded", "Degraded Film", 1, "preview skipped"),
      adminMedia(3, "processing", "Busy Film"),
    ],
    has_more: false,
  });
  const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
  await waitFor(() => expect(view.container).toHaveTextContent("Fail Film"));
  expect(view.container).toHaveTextContent("prepare exhausted");
  expect(view.container).toHaveTextContent("preview skipped");
  expect(within(view.container).getAllByRole("button", { name: /^retry$/i }).length).toBe(2);
  expect(within(view.container).queryByRole("button", { name: /^retry$/i, hidden: true }));
  // processing row: no retry near "Busy Film"
  const busy = within(view.container).getByText("Busy Film").closest(".ant-list-item");
  expect(busy).toBeTruthy();
  expect(within(busy as HTMLElement).queryByRole("button", { name: /^retry$/i })).not.toBeInTheDocument();
  expect(within(busy as HTMLElement).queryByRole("button", { name: /^remove$/i })).not.toBeInTheDocument();
});

it("retries ingest for a failed row", async () => {
  mocks.fetchAdminMedia.mockResolvedValue({
    items: [adminMedia(9, "failed", "Retry Me", 1, "boom")],
    has_more: false,
  });
  const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
  await waitFor(() => expect(view.container).toHaveTextContent("Retry Me"));
  fireEvent.click(within(view.container).getByRole("button", { name: /^retry$/i }));
  await waitFor(() => expect(mocks.retryAdminMediaIngest).toHaveBeenCalledWith(9));
});

it("removes media after confirm", async () => {
  mocks.fetchAdminMedia.mockResolvedValue({
    items: [adminMedia(8, "degraded", "Remove Me", 1, "optional failed")],
    has_more: false,
  });
  const view = render(<I18nProvider locale="en"><MemoryRouter><MediaManagerPage /></MemoryRouter></I18nProvider>);
  await waitFor(() => expect(view.container).toHaveTextContent("Remove Me"));
  fireEvent.click(within(view.container).getByRole("button", { name: /^remove$/i }));
  // Modal.confirm OK — look for Delete / confirm button in document
  await waitFor(() => expect(document.body).toHaveTextContent(/cannot be undone|Delete media/i));
  const ok = [...document.body.querySelectorAll("button")].find((b) => /delete/i.test(b.textContent || ""));
  expect(ok).toBeTruthy();
  fireEvent.click(ok!);
  await waitFor(() => expect(mocks.deleteMedia).toHaveBeenCalledWith(8));
  await waitFor(() => expect(view.container).not.toHaveTextContent("Remove Me"));
});
```

- [ ] **Step 3: Run tests — expect FAIL** (buttons / reason not implemented)

```bash
cd media/web && npx vitest run src/pages/__tests__/MediaManager.publication.test.tsx
```

Expected: new cases fail (missing buttons / error text).

- [ ] **Step 4: Commit** only if user requested commits.

---

### Task 3: Implement MediaManager ingest list UI + actions

**Files:**
- Modify: `media/web/src/pages/MediaManager.tsx`

- [ ] **Step 1: Imports**

Add `Modal` (and `Popconfirm` only if used) from `antd`. Add icons optional (`RedoOutlined`, `DeleteOutlined`) from `@ant-design/icons`.

Import from client:

```ts
deleteMedia,
fetchMediaDeletionPlan,
retryAdminMediaIngest,
```

- [ ] **Step 2: Action helpers inside `MediaManagerPage`**

Near other handlers, add:

```ts
const actionablePublication = (state: AdminMediaItem["publication_state"]) =>
  state === "failed" || state === "degraded";

async function onRetryIngest(mediaId: number) {
  try {
    await retryAdminMediaIngest(mediaId);
    message.success(t("pages.media_manager.ingest_retry_success"));
    if (libraryId !== undefined) await loadMediaPage(libraryId, undefined, false);
  } catch (e: unknown) {
    message.error((e as Error).message || t("pages.media_manager.ingest_retry_failed"));
  }
}

function onRemoveIngest(item: AdminMediaItem) {
  void (async () => {
    let files: string[] = [];
    try {
      files = await fetchMediaDeletionPlan(item.id);
    } catch {
      files = [];
    }
    Modal.confirm({
      title: t("pages.media_manager.ingest_remove_confirm_title"),
      okText: t("pages.media_manager.ingest_remove_confirm_ok"),
      okButtonProps: { danger: true },
      content: (
        <div>
          <p style={{ marginBottom: 8 }}>{t("pages.media_manager.ingest_remove_warning")}</p>
          {files.length > 0 ? (
            <ul style={{ margin: "0 0 12px", paddingLeft: 20, wordBreak: "break-all" }}>
              {files.map((f) => <li key={f}>{f}</li>)}
            </ul>
          ) : null}
        </div>
      ),
      onOk: async () => {
        try {
          await deleteMedia(item.id);
          message.success(t("pages.media_manager.ingest_remove_success"));
          setRows((prev) => prev.filter((r) => r.id !== item.id));
        } catch (e: unknown) {
          message.error((e as Error).message || t("pages.media_manager.ingest_remove_failed"));
          throw e;
        }
      },
    });
  })();
}
```

Note: `loadMediaPage` after retry resets the editor selection (existing behavior of non-append load). Acceptable per spec “refresh”.

- [ ] **Step 3: Replace ingest status Card body**

Replace the current `Card` / `List` block (~lines 765–797) with:

```tsx
<Card title={t("pages.media_manager.ingest_status_title")} loading={mediaLoading}>
  <div style={{ maxHeight: 320, overflowY: "auto" }}>
    <List
      size="small"
      dataSource={rows.filter((item) => item.publication_state !== "published")}
      locale={{ emptyText: t("pages.media_manager.ingest_status_empty") }}
      renderItem={(item) => {
        const showActions = actionablePublication(item.publication_state);
        const reason = (item.publication_error || "").trim();
        return (
          <List.Item
            actions={
              showActions
                ? [
                    <Button key="retry" type="link" size="small" aria-label={t("pages.media_manager.ingest_retry")} onClick={() => void onRetryIngest(item.id)}>
                      {t("pages.media_manager.ingest_retry")}
                    </Button>,
                    <Button key="remove" type="link" size="small" danger aria-label={t("pages.media_manager.ingest_remove")} onClick={() => onRemoveIngest(item)}>
                      {t("pages.media_manager.ingest_remove")}
                    </Button>,
                  ]
                : undefined
            }
          >
            <List.Item.Meta
              title={item.title || item.file_id}
              description={
                <Space direction="vertical" size={0} style={{ width: "100%" }}>
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>{item.file_path}</Typography.Text>
                  {showActions ? (
                    <Typography.Text type="danger" style={{ fontSize: 12 }}>
                      {reason || t("pages.media_manager.ingest_no_reason")}
                    </Typography.Text>
                  ) : null}
                </Space>
              }
            />
            <Tag
              color={item.publication_state === "processing" ? "processing" : item.publication_state === "degraded" ? "warning" : "error"}
              role="status"
              aria-label={t(`pages.media_manager.publication_${item.publication_state}`)}
            >
              {t(`pages.media_manager.publication_${item.publication_state}`)}
            </Tag>
          </List.Item>
        );
      }}
    />
  </div>
  {mediaHasMore || mediaLoadMoreError ? (
    <Button
      block
      style={{ marginTop: 8 }}
      loading={mediaLoadMoreLoading}
      aria-label={mediaLoadMoreLoading
        ? t("pages.media_manager.ingest_load_more_loading")
        : mediaLoadMoreError
          ? t("pages.media_manager.ingest_load_more_retry")
          : t("pages.media_manager.ingest_load_more")}
      onClick={() => libraryId !== undefined && void loadMediaPage(libraryId, mediaNextCursor, true)}
    >
      {mediaLoadMoreError ? t("pages.media_manager.ingest_load_more_retry") : t("pages.media_manager.ingest_load_more")}
    </Button>
  ) : null}
</Card>
```

- [ ] **Step 4: Run tests — expect PASS**

```bash
cd media/web && npx vitest run src/pages/__tests__/MediaManager.publication.test.tsx
```

Expected: all tests in file PASS. Adjust Modal OK button selector if Ant Design 6 labels differ (use `getByRole('button', { name: /delete/i })` within the modal).

- [ ] **Step 5: Commit** only if user requested.

---

### Task 4: Spec coverage check

- [ ] Scroll height 320 + overflow — Task 3
- [ ] Reason for failed/degraded — Task 3
- [ ] Retry API — Task 3
- [ ] Delete with warning — Task 3 (`Modal.confirm` + deletion plan file list)
- [ ] Actions only failed/degraded — Task 2 + 3
- [ ] Load more outside scroll — Task 3
- [ ] Tests — Task 2

---

## Self-review

1. **Spec coverage:** All design bullets mapped to Task 3; tests in Task 2.
2. **Placeholders:** None.
3. **Types:** `publication_error` on `AdminMediaItem`; APIs already exist.
4. **Commit steps:** Skipped unless user explicitly asks to commit (repo user rule).
