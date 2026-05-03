```markdown
# Knox-Media 媒体服务 需求规格说明书 + 技术方案
版本：1.0
系统：Knox 全媒体平台
子系统：knox-media（Go + React）
定位：轻量级家庭媒体服务器，兼容 Jellyfin / Emby / Nowen-Video
```

# 1. 项目概述

## 1.1 基本信息

- 项目名称：**Knox-Media**
- 开发技术：Go (Gin) + React + TypeScript + FFmpeg + SQLite
- 运行方式：**独立运行 / 微服务**
- 目标环境：Windows / Linux / macOS / Docker / 群晖 / 树莓派
- 核心对标：Jellyfin、Emby、Nowen-Video

## 1.2 核心定位

- 家庭/个人轻量级媒体中心
- 支持电影、剧集、动漫、音乐、照片、文档
- 自动刮削、实时转码、多端播放
- 提供标准 REST API 给 Knox 其他模块调用

## 1.3 技术栈

### 后端（Go）

- Web 框架：Gin
- 数据库：SQLite（轻量）/ PostgreSQL（可选）
- 媒体解析：FFmpeg + go-ffmpeg
- 文件监控：fsnotify
- 索引检索：Bleve
- 跨平台：x86 / arm64

### 前端（React）

- React 18 + TypeScript
- Ant Design / Tailwind CSS
- React Router + Zustand
- xgplayer 播放器

### 存储协议

- 本地文件 / NFS / SMB / WebDAV / S3

---

# 2. 功能清单（完整版）

## 2.1 媒体库管理

- 多媒体库支持（电影、剧集、动漫、音乐、图片、文档）
- 自定义路径、自定义刮削源、自定义扫描策略
- 启用/禁用/隐藏/权限隔离
- 支持目录自动识别季集：S01E01、Season 1、Episode 1

## 2.2 文件扫描与监控

- 全量扫描 / 增量扫描 / 定时扫描（cron）
- 文件系统实时监听（新增、删除、重命名）
- 自动识别视频、音频、字幕、图片、文档
- 自动去重（MD5、大小、名称）
- 支持损坏文件标记、异常日志

## 2.3 元数据解析与刮削

### 内置解析

- 视频：时长、分辨率、码率、编码、音轨、帧率
- 音频：歌手、专辑、流派、采样率、封面
- 图片：EXIF、拍摄时间、尺寸、GPS
- 文档：页数、标题、文本内容

### 第三方刮削

- TMDB / 豆瓣 / Bangumi（动漫）
- 刮削内容：标题、简介、海报、演员、导演、评分、上映日期
- 支持手动/批量/重试刮削
- 支持本地 NFO 读取/写入

## 2.4 播放引擎

- 原生 MP4 直接播放
- HLS 自适应流播放
- HTTP Range 断点续播
- 多音轨、多字幕、外挂字幕
- 倍速 0.5x–3x
- 画质切换：原始/1080p/720p/480p/360p
- 断点续播、播放记录同步

## 2.5 转码服务

### 实时转码

- 根据客户端网络/性能自动转码
- 支持 H.264、H.265
- 支持硬件加速（NVENC/QSV/VA-API）
- 水印、切片

### 异步转码

- 批量转码任务
- 多清晰度输出
- 任务队列、暂停、取消、重试
- 分布式调度（knox-transcode）

## 2.6 文件管理

- 网页上传、大文件分片上传、秒传、断点续传
- 远程 URL 离线下载
- 目录创建、重命名、移动、删除
- WebDAV 支持

## 2.7 用户与权限

- 多用户（管理员/普通用户/访客）
- 媒体库权限隔离
- 播放限速、并发限制
- 设备管理、日志审计

## 2.8 音乐模块

- 专辑/歌手/流派视图
- 歌词显示
- 随机/循环播放
- 封面墙

## 2.9 图片模块

- 时间轴展示
- 相册分类
- 幻灯片播放
- EXIF 查看

## 2.10 开放 API（给 Knox 平台调用）

- 媒体库管理 API
- 媒体元数据 API
- 上传 API
- 转码 API
- 播放地址 API
- 用户/历史/进度 API

---

# 3. 数据库表结构（SQLite）

```sql
-- 媒体库
CREATE TABLE library (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    type TEXT NOT NULL, -- movie,tv,anime,music,photo,document
    path TEXT NOT NULL,
    auto_scan INTEGER DEFAULT 1,
    scraper TEXT DEFAULT 'tmdb',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 媒体文件
CREATE TABLE media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    library_id INTEGER,
    file_id TEXT UNIQUE,
    title TEXT,
    original_title TEXT,
    file_path TEXT,
    file_type TEXT,
    duration INTEGER,
    width INTEGER,
    height INTEGER,
    bitrate INTEGER,
    md5 TEXT,
    format TEXT,
    meta_json TEXT,
    status TEXT DEFAULT 'active',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 剧集季
CREATE TABLE season (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tv_id INTEGER,
    season_num INTEGER,
    name TEXT,
    poster TEXT
);

-- 剧集
CREATE TABLE episode (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    season_id INTEGER,
    episode_num INTEGER,
    title TEXT,
    duration INTEGER,
    file_path TEXT
);

-- 转码任务
CREATE TABLE transcode_task (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id TEXT,
    quality TEXT,
    status TEXT DEFAULT 'waiting',
    progress INTEGER DEFAULT 0,
    output_path TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- 用户
CREATE TABLE user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT UNIQUE,
    password TEXT,
    role TEXT DEFAULT 'user'
);

-- 播放进度
CREATE TABLE play_progress (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    file_id TEXT,
    position INTEGER,
    update_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

---

# 4. API 接口文档（RESTful）

## 4.1 媒体库

- GET /api/v1/library
- POST /api/v1/library
- DELETE /api/v1/library/:id
- POST /api/v1/library/:id/scan

## 4.2 媒体信息

- GET /api/v1/media
- GET /api/v1/media/:id
- GET /api/v1/media/:id/meta
- POST /api/v1/media/:id/scrape

## 4.3 播放

- GET /api/v1/media/:id/play
- GET /api/v1/media/:id/hls
- POST /api/v1/media/:id/progress

## 4.4 上传

- POST /api/v1/upload
- POST /api/v1/upload/chunk
- POST /api/v1/upload/merge

## 4.5 转码

- POST /api/v1/transcode/async
- GET /api/v1/transcode/task
- POST /api/v1/transcode/task/:id/cancel

## 4.6 用户

- POST /api/v1/user/login
- GET /api/v1/user/info
- GET /api/v1/user/history

---

# 5. Go 后端项目结构

```
knox-media/
├── cmd/
│   └── server.go        # 入口
├── api/
│   ├── handler/         # HTTP 处理器
│   ├── middleware/      # 鉴权/跨域
│   └── router.go        # 路由
├── config/              # 配置
├── internal/
│   ├── library/         # 媒体库逻辑
│   ├── media/           # 媒体解析
│   ├── scanner/         # 扫描器
│   ├── scraper/         # 刮削
│   ├── transcode/       # 转码
│   └── upload/          # 上传
├── pkg/
│   ├── ffprobe/         # 媒体解析
│   ├── fileutil/
│   └── hashutil/
├── config.yml
├── go.mod
└── Dockerfile
```

---

# 6. React 前端结构

```
knox-media-web/
├── src/
│   ├── pages/
│   │   ├── Home
│   │   ├── Library
│   │   ├── Media
│   │   ├── Player
│   │   ├── Upload
│   │   └── Settings
│   ├── components/
│   ├── api/
│   ├── store/
│   └── App.tsx
├── package.json
└── vite.config.ts
```

---

# 7. Docker 部署脚本

## docker-compose.yml

```yaml
version: '3.8'
services:
  knox-media:
    image: knox-media:latest
    ports:
      - "8200:8200"
    volumes:
      - ./data:/app/data
      - /media:/media
    restart: always
```

---

# 8. 部署说明

- 端口：8200
- 配置文件：config.yml
- 媒体目录：/media
- 数据库：data/knox-media.db
- 转码缓存：data/transcode

---

## DRM Packaging (2026-04)

- Media library now supports two policy toggles: `drm_enabled` and `cleanup_local_source_after_package`.
- For DRM-enabled libraries, new videos enqueue `cmaf_drm` package tasks and expose unified HLS DRM playback contract.
- Playback info may return `mode=hls_drm` with:
  - `widevine_license_url`
  - `fairplay_cert_url`
  - `fairplay_license_url`
- Built-in license endpoints:
  - `POST /api/v1/drm/widevine/license`
  - `GET /api/v1/drm/fairplay/cert`
  - `POST /api/v1/drm/fairplay/license`
- Admin verify endpoint (debug/introspection):
  - `POST /api/v1/admin/drm/license/verify`
  - request: `{ "license": "<base64 payload>", "sig": "<base64 signature>" }`
  - success: `{ "valid": true, "claims": {...}, "canonical": "drm|media|..." }`
  - failure: `{ "valid": false, "error": "...", "code": "..." }`
    - codes: `signature_mismatch`, `kid_version_mismatch`, `sig_version_mismatch`, `license_expired`, `invalid_payload`, `verify_failed`
- Source cleanup guard is upload-local only (external mounted media never deleted by cleanup policy).

