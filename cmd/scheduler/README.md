# Scheduler 架构说明（完全即时处理）

## 方案实现

- ✅ 零预处理：首次请求时才触发切片
- ✅ 按需切片：可以先只切前几个片段，后续按需切分
- ✅ 分布式切片：多节点协作切片
- ✅ 即时转码：按需转码，支持优先级队列
- ✅ 断流停止：会话监控，自动中止无用转码
- ✅ 自动清理：LRU + TTL 清理缓存

## 核心设计理念

```text
源文件 → 按需切片 → 按需转码 → 按需清理
         ↓           ↓           ↓
      分布式调度   分布式Worker   LRU缓存
```

## 整体架构

```text
┌─────────────────────────────────────────────────────────────────┐
│                         客户端请求                              │
│                    GET /master/{file_id}                       │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Scheduler (调度节点)                         │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ 1. 检查源文件是否存在                                   │    │
│  │ 2. 检查是否已有切片索引（Redis）                        │    │
│  │ 3. 没有则启动切片任务（分布式锁）                       │    │
│  │ 4. 返回 Master Playlist                                 │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Slice Worker (切片节点)                      │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ - 分析关键帧位置                                        │    │
│  │ - 切分为 MKV 片段（音视频分离）                         │    │
│  │ - 存储到共享存储（S3/NFS）                              │    │
│  │ - 生成索引并保存到 Redis                                │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Transcode Worker (转码节点)                   │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │ - 订阅转码任务                                          │    │
│  │ - MKV 实时转码为 TS                                     │    │
│  │ - 缓存到共享存储                                        │    │
│  │ - 更新切片状态                                          │    │
│  └─────────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

## 启动方式（双模式兼容）

### 模式 A：内嵌模式（推荐）

在 `server` 主程序内自动启动 `Scheduler`、`SliceWorker`、`TranscodeWorker`。

```bash
# 1. 启动 Redis
docker run -d -p 6379:6379 redis:7-alpine

# 2. 启动主服务（会内嵌启动即时转码三组件）
./bin/server
```

### 模式 B：独立进程模式（兼容旧部署）

适用于分布式扩展、单独扩容 Worker 或灰度迁移。

```bash
# 1. 启动 Redis
docker run -d -p 6379:6379 redis:7-alpine

# 2. 启动 Scheduler
./bin/schedulerd

# 3. 启动 Slice Worker（可多实例）
./bin/sliceworkerd

# 4. 启动 Transcode Worker（可多实例）
./bin/transcodeworkerd
```

### 关键环境变量

```bash
# Redis 地址（默认 127.0.0.1:6379）
KNOX_MEDIA_REDIS_ADDR=127.0.0.1:6379

# 配置文件路径（默认 config.yml）
KNOX_MEDIA_CONFIG=./config.yml

# 仅独立模式可选：Scheduler 监听地址（默认 :8300）
KNOX_MEDIA_SCHEDULER_ADDR=:8300

# 仅独立模式可选：Worker 标识
KNOX_MEDIA_SLICE_WORKER_ID=slice-1
KNOX_MEDIA_TRANSCODE_WORKER_ID=transcode-1

# 仅独立模式可选：转码并发（默认 2）
KNOX_MEDIA_TRANSCODE_MAX_CONCURRENT=4
```

## 启动所有服务（旧命名示例）

```bash
# 1. 启动 Redis
docker run -d -p 6379:6379 redis:7-alpine

# 2. 启动调度服务（多个实例）
./bin/scheduler --config configs/scheduler.yaml

# 3. 启动切片 Worker（多个实例）
./bin/sliceworker --config configs/sliceworker.yaml

# 4. 启动转码 Worker（多个实例）
./bin/transcodeworker --config configs/transcodeworker.yaml
```

> 说明：`scheduler/sliceworker/transcodeworker` 为早期命名，当前建议使用 `schedulerd/sliceworkerd/transcodeworkerd` 作为独立进程入口。

## 客户端触发规则（新增）

当客户端请求播放信息（`/api/v1/media/:id/hls`）时：

- 如果客户端声明能力可直接播放源文件（容器与音视频编码均支持），返回 `native`。
- 如果客户端不支持源容器/编码：
  - 自动切换到即时转码链路 `jit_hls`
  - 返回即时播放入口：`/api/v1/jit/master/{file_id}`
  - 后续由 Scheduler 按需触发切片与转码。

## JIT 链路请求路径

```text
GET /api/v1/jit/master/{file_id}
GET /api/v1/jit/playlist/{file_id}/video/{bitrate}
GET /api/v1/jit/playlist/{file_id}/audio/{lang}
GET /api/v1/jit/segment/{file_id}/video/{bitrate}/{seg_id}
GET /api/v1/jit/segment/{file_id}/audio/{lang}/{seg_id}
```

## 完整请求流程

1. 用户首次请求 Master Playlist  
   ↓
2. Scheduler 检查视频是否切片
   - 未切片 -> 发布切片任务 -> 返回 `slicing` 状态等待
   - 切片中 -> 等待完成
   - 已切片 -> 直接返回 Master Playlist  
   ↓
3. 切片 Worker 处理切片任务
   - 分析关键帧
   - 切分 MKV 片段
   - 切分音频片段
   - 保存索引到 Redis  
   ↓
4. 客户端请求 Video Playlist
   - Scheduler 返回完整 Playlist（包含所有切片 URL）  
   ↓
5. 客户端请求第一个 TS 切片
   - Scheduler 检查 TS 是否存在
   - 不存在 -> 发布转码任务 -> 等待
   - 转码 Worker 处理任务 -> 保存 TS
   - Scheduler 返回 TS 切片  
   ↓
6. 后续切片
   - 已转码的秒回
   - 未转码的触发转码（可能已有预取任务）  
   ↓
7. 用户 Seek
   - 客户端根据 Playlist 直接请求新切片
   - Scheduler 触发该切片转码（如果未转码）

## Redis 数据结构

### 视频元数据

```text
video:meta:{file_id} -> hash {
    "file_path": "/videos/movie.mp4",
    "duration": "3600.5",
    "status": "slicing/ready/failed",
    "width": "1920",
    "height": "1080"
}
```

### 切片索引（JSON 存储）

```text
video:index:{file_id} -> string (JSON)
```

### 切片任务锁（防止重复切片）

```text
lock:slice:{file_id} -> string (TTL: 300s)
```

### 转码任务队列（优先级队列）

```text
transcode:queue:high -> sorted set (priority -> task_json)
transcode:queue:normal -> sorted set
transcode:queue:low -> sorted set
```

### 转码任务锁（防止重复转码）

```text
lock:transcode:{file_id}:{bitrate}:{segment_id} -> string (TTL: 120s)
```

### 切片转码状态

```text
segment:status:{file_id}:{segment_id}:{bitrate} -> string (pending/transcoding/ready/failed)
```

### 会话管理

```text
session:{session_id} -> hash {
    "file_id": "xxx",
    "bitrate": "2000k",
    "last_active": timestamp,
    "current_segment": 5
}
TTL: 35s
```

### 切片访问统计（用于 LRU 清理）

```text
segment:access:{file_id}:{bitrate}:{segment_id} -> hash {
    "last_access": timestamp,
    "access_count": 0,
    "size": 0
}
```

### 全局转码统计

```text
transcode:stats:{file_id}:{bitrate} -> hash {
    "total": 0,
    "completed": 0,
    "failed": 0
}
```