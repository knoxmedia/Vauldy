# Multi-Language Support Specification
# 多语言支持需求规格（个人 / 家庭私密影音系统）

> **目标读者**：Cursor Agent / 开发工程师  
> **版本**：v1.0  
> **范围**：UI 国际化（i18n）+ 视频音轨 / 字幕语言联动

---

## 1. 概述 Overview

系统需要为**管理员**和**普通用户**提供独立的界面语言设置，并在视频播放时自动优先选择与用户界面语言一致的音轨和字幕。

The system shall support per-role language preferences: administrators configure a system-wide default; regular users set a personal preferred language that also drives audio track and subtitle selection during playback.

---

## 2. 角色与语言入口 Roles & Language Entry Points

| 角色 Role | 设置路径 Setting Path | 设置项 Field |
|---|---|---|
| 管理员 Admin | 系统选项 → 通用 *System Options → General* | 首选显示语言 *Preferred Display Language* |
| 普通用户 User | 账号设置 *Account Settings* | 界面语言（将同步首选音轨 / 字幕语言）*Interface Language (syncs preferred audio track / subtitle language)* |

---

## 3. 功能需求 Functional Requirements

### 3.1 管理员 — 首选显示语言

- **FR-ADM-01**：管理员在「系统选项 → 通用」页面看到下拉选择器「首选显示语言」。
- **FR-ADM-02**：可选语言最少包括：`zh-CN`（简体中文）、`zh-TW`（繁体中文）、`en`（English）、`ja`（日本語）、`ko`（한국어）。项目可按需扩展。
- **FR-ADM-03**：管理员保存设置后，**后台管理界面**（Admin UI）立即切换到所选语言，无需刷新页面。
- **FR-ADM-04**：该设置仅影响管理后台界面，不影响已登录普通用户的语言偏好。
- **FR-ADM-05**：系统将管理员语言偏好持久化到服务端（管理员账号维度），以便不同设备登录时保持一致。

### 3.2 普通用户 — 界面语言

- **FR-USR-01**：普通用户在「账号设置」页面看到下拉选择器，标签文本为「界面语言（将同步首选音轨 / 字幕语言）」。
- **FR-USR-02**：可选语言集合与管理员一致（见 FR-ADM-02）。
- **FR-USR-03**：用户保存后，其所见的**所有前台界面**（浏览、搜索、播放器 UI、通知等）立即切换到所选语言，无需刷新页面。
- **FR-USR-04**：语言偏好持久化到服务端（用户账号维度），多端登录保持一致。
- **FR-USR-05**：用户登出后，下次登录时自动恢复该用户的语言偏好。

### 3.3 视频播放 — 音轨与字幕联动

- **FR-PLAY-01**：播放器初始化时，读取**当前登录用户**的界面语言偏好。
- **FR-PLAY-02**：**音轨选择**：优先激活与用户语言匹配的音轨（按 BCP-47 语言标签匹配，如 `zh`、`zh-CN`、`cmn`）；若无匹配音轨，则回退到视频默认音轨。
- **FR-PLAY-03**：**字幕选择**：优先激活与用户语言匹配的字幕轨道；若无匹配字幕，则回退到「无字幕」（不自动选择其他语言字幕）。
- **FR-PLAY-04**：用户在播放过程中手动切换音轨或字幕后，本次播放会话以手动选择为准，但**不修改**账号级语言偏好。
- **FR-PLAY-05**：语言匹配优先级：`精确匹配（zh-CN）> 主语言匹配（zh）> 默认轨道`。

---

## 4. 非功能需求 Non-Functional Requirements

| ID | 描述 |
|---|---|
| NFR-01 | 语言资源文件采用标准 **i18n JSON** 格式，键名使用点分命名空间（如 `settings.language.label`）。 |
| NFR-02 | 新增语言只需新增对应 JSON 文件，无需修改业务逻辑代码。 |
| NFR-03 | 语言切换动作（前端状态更新 + API 调用）须在 **300 ms** 内完成感知响应。 |
| NFR-04 | 未翻译的字符串**回退到英文**（`en`），禁止展示裸露的键名（如 `settings.language.label`）。 |
| NFR-05 | 管理员语言偏好与普通用户语言偏好存储在**各自的账号记录**中，不共享字段。 |

---

## 5. 数据模型变更 Data Model Changes

```
// 用户表新增字段（普通用户 & 管理员账号表均需添加）
preferredLanguage: string   // BCP-47，如 "zh-CN"、"en"、"ja"
                             // 默认值：系统安装时由管理员设置的 preferredLanguage
```

---

## 6. API 接口 API Endpoints

```
// 更新当前用户语言偏好
PATCH /api/user/me/preferences
Body: { "preferredLanguage": "zh-CN" }
Response: 200 OK

// 管理员更新系统语言设置
PATCH /api/admin/settings/general
Body: { "preferredDisplayLanguage": "en" }
Response: 200 OK
```

---

## 7. UI 文案要求 UI Copy Requirements

| 位置 Location | 字段标签（中文）| Field Label (English) |
|---|---|---|
| 系统选项 → 通用 | 首选显示语言 | Preferred Display Language |
| 账号设置 | 界面语言（将同步首选音轨 / 字幕语言）| Interface Language (syncs preferred audio track / subtitle language) |
| 两处均需 | 下拉占位符：请选择语言 | Placeholder: Select a language |
| 保存成功提示 | 语言设置已保存 | Language preference saved |

---

## 8. 验收标准 Acceptance Criteria

- [ ] 管理员切换语言 → 后台 Admin UI 全部文字同步变更，前台用户界面不受影响。
- [ ] 普通用户切换语言 → 其前台界面全部文字同步变更，其他用户界面不受影响。
- [ ] 重新登录后语言偏好与上次设置一致。
- [ ] 播放含多语言音轨的视频 → 自动选中与用户语言匹配的音轨。
- [ ] 播放含多语言字幕的视频 → 自动激活匹配字幕；无匹配时不显示字幕。
- [ ] 手动切换音轨 / 字幕后，账号级语言偏好不变。
- [ ] 添加新语言 JSON 文件后，无需修改代码即可在下拉列表中出现。

---

*End of Specification*
