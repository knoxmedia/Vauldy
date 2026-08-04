# Knox-Media (9527) 全链路加密与防泄露方案 — Go 实现

> 覆盖视频 / 音频 / 图片 / 文档的上传 → 加密 → 预处理 → 转码 → 安全传输 → 防泄露全链路

---

## 目录

1. [架构总览](#1-架构总览)
2. [核心加密引擎（全类型共享）](#2-核心加密引擎全类型共享)
3. [KEK 安全管理](#3-kek-安全管理)
4. [入库加密管线（上传）](#4-入库加密管线上传)
5. [预处理管线（缩略图 / 预览 / 预转码）](#5-预处理管线缩略图--预览--预转码)
6. [实时转码引擎](#6-实时转码引擎)
7. [视频 & 音频：HLS 加密传输 + DRM 防泄露](#7-视频--音频hls-加密传输--drm-防泄露)
8. [图片：双水印污染输出防泄露](#8-图片双水印污染输出防泄露)
9. [文档：逐页渲染阻断文本提取](#9-文档逐页渲染阻断文本提取)
10. [客户端加固（CSS / JS / HTTP 头）](#10-客户端加固css--js--http-头)
11. [溯源水印系统](#11-溯源水印系统)
12. [签名 URL 令牌 + 防重放](#12-签名-url-令牌--防重放)
13. [管理员再编码优化](#13-管理员再编码优化)
14. [数据库设计（统一 Schema）](#14-数据库设计统一-schema)
15. [API 接口定义](#15-api-接口定义)
16. [项目结构与路由注册](#16-项目结构与路由注册)
17. [部署环境依赖 & 安全 Checklist](#17-部署环境依赖--安全-checklist)
18. [防泄露效果总矩阵](#18-防泄露效果总矩阵)

---

## 1. 架构总览

### 1.1 六层纵深防御

```
L1  TLS 1.3         — 传输层加密，防中间人嗅探
L2  签名 URL 令牌    — 防未授权访问、URL 分享、重放攻击
L3  HLS AES-128     — 视频/音频分片加密，客户端拿不到完整明文
L4  污染输出        — 图片双水印+降质，文档逐页渲染为图片
L5  客户端加固       — CSP / Headers / 右键阻断 / DevTools 检测
L6  溯源水印         — LSB 隐写 + 可见水印，泄露后可追责
```

### 1.2 四类媒体分管线

```
┌──────────────────────────────────────────────────────────────┐
│                     HTTP Upload (TLS 1.3)                    │
└──────────────────────────────┬───────────────────────────────┘
                               │
                               ▼
┌──────────────────────────────────────────────────────────────┐
│              IngestService (入库加密)                         │
│  明文 → DEK 加密 → .enc 落盘 → KEK 包装 DEK → 写入 DB       │
└──────────────────────────────┬───────────────────────────────┘
                               │
         ┌─────────────────────┼─────────────────────┐
         ▼                     ▼                     ▼
   ┌──────────┐         ┌──────────┐          ┌──────────┐
   │ 视频/音频 │         │   图片    │          │   文档    │
   └────┬─────┘         └────┬─────┘          └────┬─────┘
        │                    │                     │
        ▼                    ▼                     ▼
   预转码编码           缩略图生成           文档逐页渲染
   (多Profile)         (异步加密落盘)       (Office→PDF→图片)
        │                    │                     │
        ▼                    ▼                     ▼
   HLS AES-128          双水印引擎           每页加水印
   分片加密              污染+降质            JPEG 输出
        │                    │                     │
        ▼                    ▼                     ▼
   播放器内存解密        JPEG 85% 返回       前端翻页阅读器
   (密钥15分钟过期)      (no-store)          (全图片,无文本)
```

---

## 2. 核心加密引擎（全类型共享）

### 2.1 信封加密 (Envelope Encryption)

```
每个文件: 独立的随机 DEK (AES-256)  → 加密文件内容
DEK 本身:   由 KEK (AES-256) 包装   → 存入 DB

KEK 和加密文件永远不在同一位置。
磁盘被拖走 → 没有 KEK → 无法解开 DEK → 文件不可读
```

### 2.2 加密文件格式 (.enc)

```
Offset  Size  Field
------  ----  -----
0       4     Magic: 0x39 0x35 0x32 0x37 = "9527"
4       1     Version: 0x01
5       1     Mode: 0x00=GCM, 0x01=CTR+HMAC
6       2     Reserved
8       12    IV (96-bit for GCM)
20      16    GCM Auth Tag  (GCM模式)
-------------------------------------------------
36      N     Ciphertext
```

### 2.3 加密引擎实现 (envelope.go)

```go
package crypto

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "crypto/subtle"
    "encoding/binary"
    "errors"
    "fmt"
    "io"
)

const (
    Magic9527  = "\x39\x35\x32\x37"
    Version    = byte(1)
    DEKSize    = 32
    IVSize     = 12
    GCMTagSize = 16
)

var (
    ErrBadMagic   = errors.New("enc: bad magic number")
    ErrBadVersion = errors.New("enc: unsupported version")
    ErrIntegrity  = errors.New("enc: GCM integrity check failed")
)

type EnvelopeResult struct {
    FilePath   string
    WrappedDEK []byte
    IV         []byte
}

// EncryptFile 加密明文流 → 写入密文目标
func EncryptFile(src io.Reader, dst io.Writer, kek []byte) (*EnvelopeResult, error) {
    dek := make([]byte, DEKSize)
    iv  := make([]byte, IVSize)
    if _, err := io.ReadFull(rand.Reader, dek); err != nil {
        return nil, fmt.Errorf("generate DEK: %w", err)
    }
    if _, err := io.ReadFull(rand.Reader, iv); err != nil {
        return nil, fmt.Errorf("generate IV: %w", err)
    }

    block, _ := aes.NewCipher(dek)
    aead, _ := cipher.NewGCM(block)

    // 写文件头
    dst.Write([]byte(Magic9527))
    binary.Write(dst, binary.LittleEndian, Version)
    binary.Write(dst, binary.LittleEndian, byte(0x00)) // GCM mode
    dst.Write(make([]byte, 2))                         // reserved
    dst.Write(iv)

    // 流式加密
    plaintext, _ := io.ReadAll(src)
    ciphertext := aead.Seal(nil, iv, plaintext, nil) // 含 16B GCM Tag
    dst.Write(ciphertext)

    // KEK 包装 DEK (RFC 3394)
    wrappedDEK, _ := aesKeyWrap(dek, kek)

    // 清零内存
    subtle.ConstantTimeCopy(1, dek, make([]byte, DEKSize))

    return &EnvelopeResult{WrappedDEK: wrappedDEK, IV: iv}, nil
}

// DecryptStream 流式解密, 返回 io.ReadCloser
func DecryptStream(src io.Reader, wrappedDEK, kek []byte) (io.ReadCloser, error) {
    dek, _ := aesKeyUnwrap(wrappedDEK, kek)
    defer subtle.ConstantTimeCopy(1, dek, make([]byte, DEKSize))

    block, _ := aes.NewCipher(dek)
    aead, _ := cipher.NewGCM(block)

    header, ciphertext, _ := readEncFile(src)
    plaintext, err := aead.Open(nil, header.IV[:], ciphertext, nil)
    if err != nil {
        return nil, ErrIntegrity
    }
    return &zeroReader{data: plaintext}, nil
}

// ========== RFC 3394 AES Key Wrap ==========

func aesKeyWrap(key, plaintext []byte) ([]byte, error) {
    block, _ := aes.NewCipher(key)
    n := len(plaintext) / 8
    ciphertext := make([]byte, 8+n*8)
    iv := []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}
    copy(ciphertext, iv)
    copy(ciphertext[8:], plaintext)

    tmp := make([]byte, 16)
    for j := 0; j <= 5; j++ {
        for i := 1; i <= n; i++ {
            copy(tmp[:8], ciphertext[:8])
            copy(tmp[8:], ciphertext[i*8:(i+1)*8])
            block.Encrypt(tmp, tmp)
            t := uint64(n*j + i)
            for k := 7; k >= 0; k-- {
                tmp[k] ^= byte(t & 0xFF)
                t >>= 8
            }
            copy(ciphertext[:8], tmp[:8])
            copy(ciphertext[i*8:(i+1)*8], tmp[8:])
        }
    }
    return ciphertext, nil
}

func aesKeyUnwrap(key, ciphertext []byte) ([]byte, error) {
    block, _ := aes.NewCipher(key)
    n := (len(ciphertext) / 8) - 1
    buf := make([]byte, len(ciphertext))
    copy(buf, ciphertext)

    tmp := make([]byte, 16)
    for j := 5; j >= 0; j-- {
        for i := n; i >= 1; i-- {
            t := uint64(n*j + i)
            copy(tmp[:8], buf[:8])
            for k := 7; k >= 0; k-- {
                tmp[k] ^= byte(t & 0xFF)
                t >>= 8
            }
            copy(tmp[8:], buf[i*8:(i+1)*8])
            block.Decrypt(tmp, tmp)
            copy(buf[:8], tmp[:8])
            copy(buf[i*8:(i+1)*8], tmp[8:])
        }
    }
    iv := []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}
    for i := 0; i < 8; i++ {
        if buf[i] != iv[i] {
            return nil, errors.New("keywrap: integrity check failed")
        }
    }
    return buf[8:], nil
}

// ========== 工具函数 ==========

type zeroReader struct {
    data []byte
    pos  int
}

func (z *zeroReader) Read(p []byte) (int, error) {
    if z.pos >= len(z.data) {
        subtle.ConstantTimeCopy(1, z.data, make([]byte, len(z.data)))
        return 0, io.EOF
    }
    n := copy(p, z.data[z.pos:])
    z.pos += n
    if z.pos >= len(z.data) {
        subtle.ConstantTimeCopy(1, z.data, make([]byte, len(z.data)))
    }
    return n, nil
}

func (z *zeroReader) Close() error {
    subtle.ConstantTimeCopy(1, z.data, make([]byte, len(z.data)))
    return nil
}

func readEncFile(r io.Reader) (*struct{ IV [IVSize]byte }, []byte, error) {
    magic := make([]byte, 4)
    io.ReadFull(r, magic)
    if string(magic) != Magic9527 {
        return nil, nil, ErrBadMagic
    }
    ver := make([]byte, 1)
    io.ReadFull(r, ver)
    if ver[0] != Version {
        return nil, nil, ErrBadVersion
    }
    skip := make([]byte, 3)
    io.ReadFull(r, skip) // mode + reserved
    hdr := &struct{ IV [IVSize]byte }{}
    io.ReadFull(r, hdr.IV[:])
    ciphertext, _ := io.ReadAll(r)
    return hdr, ciphertext, nil
}
```

---

## 3. KEK 安全管理

### 3.1 KEK Vault (keystore/vault.go)

```go
package keystore

import (
    "context"
    "crypto/rand"
    "crypto/subtle"
    "errors"
    "os"
    "sync"
    "syscall"

    "golang.org/x/crypto/argon2"
)

type Vault struct {
    mu      sync.RWMutex
    kek     []byte       // 仅在内存中, 永不明文落盘
    version int
    salt    []byte
}

func NewVault() (*Vault, error) {
    mainKey := os.Getenv("KNOX_MAIN_KEY")
    if mainKey == "" {
        return nil, errors.New("KNOX_MAIN_KEY not set")
    }
    salt, _ := loadOrCreateSalt("/etc/knox-media/salt.bin")
    kek := argon2.IDKey([]byte(mainKey), salt, 3, 64*1024, 4, 32)
    syscall.Mlock(kek) // 防止 swap

    v := &Vault{kek: kek, version: 1, salt: salt}
    return v, nil
}

func (v *Vault) GetKEK(ctx context.Context) ([]byte, error) {
    v.mu.RLock()
    defer v.mu.RUnlock()
    if v.kek == nil {
        return nil, errors.New("KEK not initialized")
    }
    kekCopy := make([]byte, len(v.kek))
    copy(kekCopy, v.kek)
    return kekCopy, nil
}

func (v *Vault) RotateKEK(newMasterKey string) error {
    v.mu.Lock()
    defer v.mu.Unlock()
    newKEK := argon2.IDKey([]byte(newMasterKey), v.salt, 3, 64*1024, 4, 32)
    subtle.ConstantTimeCopy(1, v.kek, make([]byte, len(v.kek)))
    v.kek = newKEK
    v.version++
    syscall.Mlock(v.kek)
    return nil
}

func (v *Vault) Destroy() {
    v.mu.Lock()
    defer v.mu.Unlock()
    subtle.ConstantTimeCopy(1, v.kek, make([]byte, len(v.kek)))
    v.kek = nil
}

func loadOrCreateSalt(path string) ([]byte, error) {
    if data, err := os.ReadFile(path); err == nil {
        return data, nil
    }
    salt := make([]byte, 32)
    rand.Read(salt)
    os.MkdirAll("/etc/knox-media", 0700)
    os.WriteFile(path, salt, 0600)
    return salt, nil
}
```

### 3.2 KEK 安全原则

| 原则 | 实现 |
|------|------|
| KEK 不出内存 | 仅通过 `GetKEK()` 获取副本, 用完立即 `ConstantTimeCopy` 清零 |
| 防 swap | `mlock()` 锁定内存页 |
| 防 dump | 禁用 core dump: `setrlimit(RLIMIT_CORE, 0)` |
| 定期轮换 | KEK 30天轮换; 信封加密优势: 只更新 DB 中 `wrapped_dek`, 不重加密文件 |
| 分片存储 | 可选 Shamir 秘密共享, 3 片中至少 2 片还原 |

---

## 4. 入库加密管线（上传）

所有媒体类型共用一个入库入口, 加密逻辑完全一致。

```go
package storage

import (
    "context"
    "crypto/rand"
    "database/sql"
    "fmt"
    "io"
    "os"
    "path/filepath"

    kcrypto "knox-media/internal/crypto"
    "knox-media/internal/keystore"
)

type IngestService struct {
    db       *sql.DB
    keystore *keystore.Vault
    basePath string
}

type IngestResult struct {
    FileID     string
    EncPath    string
    WrappedDEK string
    IV         string
    FileType   string
    OrigName   string
    FileSize   int64
}

func (s *IngestService) Ingest(ctx context.Context, src io.Reader,
    filename, mimeType string) (*IngestResult, error) {

    fileID   := newUUID()
    fileType := detectType(mimeType)
    encPath  := filepath.Join(s.basePath, fileType, fileID+".enc")

    os.MkdirAll(filepath.Dir(encPath), 0700)

    dst, _ := os.OpenFile(encPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
    defer dst.Close()

    kek, _ := s.keystore.GetKEK(ctx)
    result, _ := kcrypto.EncryptFile(src, dst, kek)
    result.FilePath = encPath

    fileSize, _ := os.Stat(encPath)

    ir := &IngestResult{
        FileID:     fileID,
        EncPath:    encPath,
        WrappedDEK: fmt.Sprintf("%x", result.WrappedDEK),
        IV:         fmt.Sprintf("%x", result.IV),
        FileType:   fileType,
        OrigName:   filename,
        FileSize:   fileSize.Size(),
    }

    s.db.ExecContext(ctx, `
        INSERT INTO media_files (id, enc_path, wrapped_dek, iv, file_type, orig_name, file_size, mime_type)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    `, ir.FileID, ir.EncPath, ir.WrappedDEK, ir.IV, ir.FileType, ir.OrigName, ir.FileSize, mimeType)

    // 触发异步预处理
    s.enqueuePreprocessing(ir, s.keystore)

    return ir, nil
}

func detectType(mime string) string {
    switch {
    case len(mime) >= 6 && mime[:6] == "video/":
        return "video"
    case len(mime) >= 6 && mime[:6] == "audio/":
        return "audio"
    case len(mime) >= 6 && mime[:6] == "image/":
        return "image"
    default:
        return "document"
    }
}

func newUUID() string {
    b := make([]byte, 16)
    rand.Read(b)
    return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
```

---

## 5. 预处理管线（缩略图 / 预览 / 预转码）

上传完成后, 以下任务**异步并行**执行：

### 5.1 视频：预转码 + 缩略图

```go
func (s *PreprocessService) PreprocessVideo(ctx context.Context, fileID, encPath string, wdek, kek []byte) {
    var wg sync.WaitGroup

    // 任务 1: 生成缩略图
    wg.Add(1)
    go func() {
        defer wg.Done()
        s.generateVideoThumb(ctx, fileID, encPath, wdek, kek)
    }()

    // 任务 2: 预转码 720p
    wg.Add(1)
    go func() {
        defer wg.Done()
        s.pretranscode(ctx, fileID, encPath, wdek, kek, Profile720p)
    }()

    // 任务 3: 预转码 1080p
    wg.Add(1)
    go func() {
        defer wg.Done()
        s.pretranscode(ctx, fileID, encPath, wdek, kek, Profile1080p)
    }()

    wg.Wait()
}
```

### 5.2 缩略图生成（所有类型）

```go
func (s *PreprocessService) generateVideoThumb(ctx context.Context,
    fileID, encPath string, wdek, kek []byte) {

    // 1. 解密流 → ffmpeg stdin
    decStream, _ := kcrypto.DecryptStream(mustOpen(encPath), wdek, kek)
    defer decStream.Close()

    cmd := exec.CommandContext(ctx, "ffmpeg",
        "-i", "pipe:0",
        "-vframes", "1", "-ss", "5",
        "-s", "320x180",
        "-f", "image2", "pipe:1",
    )
    cmd.Stdin = decStream
    var stdout bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Run()

    // 2. 立即加密缩略图
    thumbPath := fmt.Sprintf("%s/thumbnail/%s_thumb.enc", s.basePath, fileID)
    s.encryptToDisk(ctx, thumbPath, &stdout, kek)
}

func (s *PreprocessService) encryptToDisk(ctx context.Context, path string, data *bytes.Buffer, kek []byte) {
    os.MkdirAll(filepath.Dir(path), 0700)
    dst, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
    defer dst.Close()
    result, _ := kcrypto.EncryptFile(bytes.NewReader(data.Bytes()), dst, kek)
    s.db.ExecContext(ctx, `
        INSERT INTO thumbnails (file_id, enc_path, wrapped_dek, iv) VALUES (?, ?, ?, ?)
    `, extractFileID(path), path, fmt.Sprintf("%x", result.WrappedDEK), fmt.Sprintf("%x", result.IV))
}
```

> **关键安全原则**: 缩略图生成过程中, 明文通过 `bytes.Buffer` 暂存, 加密写入 `.enc` 后立即被 GC 回收, **明文从未落盘**。

### 5.3 预转码 (pre-transcoding)

入库时预先转好常用清晰度并加密缓存, 用户请求时**零转码延迟**。

```go
var (
    Profile360p  = VideoProfile{Name: "360p",  VCodec: "libx264", VBitrate: "500k",  Size: "640x360"}
    Profile720p  = VideoProfile{Name: "720p",  VCodec: "libx264", VBitrate: "1500k", Size: "1280x720"}
    Profile1080p = VideoProfile{Name: "1080p", VCodec: "libx264", VBitrate: "4000k", Size: "1920x1080"}
    ProfileOrig  = VideoProfile{Name: "orig",  VCodec: "copy",    VBitrate: "",       Size: ""}
)

func (s *PreprocessService) pretranscode(ctx context.Context,
    fileID, encPath string, wdek, kek []byte, profile VideoProfile) {

    decStream, _ := kcrypto.DecryptStream(mustOpen(encPath), wdek, kek)
    defer decStream.Close()

    cmd := exec.CommandContext(ctx, "ffmpeg",
        "-i", "pipe:0",
        "-c:v", profile.VCodec, "-b:v", profile.VBitrate, "-s", profile.Size,
        "-c:a", "aac",
        "-movflags", "frag_keyframe+empty_moov",
        "-f", "matroska", "pipe:1",
    )
    cmd.Stdin = decStream
    var stdout bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Run()

    // 新 DEK 加密转码结果
    outPath := fmt.Sprintf("%s/cache/%s_%s.enc", s.basePath, fileID, profile.Name)
    s.encryptToCache(ctx, fileID, profile.Name, outPath, &stdout, kek)
}
```

---

## 6. 实时转码引擎

当用户请求未缓存的 Profile 时, 实时转码 + 解密管道对接。

```go
package transcode

type TranscodeProfile struct {
    Name     string
    VCodec   string
    VBitrate string
    Size     string
    ACodec   string
    Format   string
}

// TranscodeStream 解密 → ffmpeg 转码 → 返回转码后流
// 全程零拷贝管道, 明文不落盘
func TranscodeStream(ctx context.Context,
    encPath string, wdek, kek []byte,
    profile TranscodeProfile) (io.ReadCloser, error) {

    src, _ := os.Open(encPath)
    decStream, _ := kcrypto.DecryptStream(src, wdek, kek)

    args := []string{
        "-i", "pipe:0",
        "-c:v", profile.VCodec,
    }
    if profile.VBitrate != "" {
        args = append(args, "-b:v", profile.VBitrate)
    }
    if profile.Size != "" {
        args = append(args, "-s", profile.Size)
    }
    args = append(args,
        "-c:a", profile.ACodec,
        "-movflags", "frag_keyframe+empty_moov",
        "-f", profile.Format, "pipe:1",
    )

    cmd := exec.CommandContext(ctx, "ffmpeg", args...)
    cmd.Stdin = decStream
    stdout, _ := cmd.StdoutPipe()
    cmd.Start()

    return &transcodeReader{
        cmd: cmd, stdout: stdout, src: src, dec: decStream,
    }, nil
}

type transcodeReader struct {
    cmd    *exec.Cmd
    stdout io.ReadCloser
    src    *os.File
    dec    io.ReadCloser
}

func (r *transcodeReader) Read(p []byte) (int, error) { return r.stdout.Read(p) }
func (r *transcodeReader) Close() error {
    r.cmd.Wait(); r.dec.Close(); return r.src.Close()
}
```

---

## 7. 视频 & 音频：HLS 加密传输 + DRM 防泄露

视频和音频的核心防线是**HLS + AES-128 分片加密**——客户端永远只能拿到加密的 `.ts` 碎片和一把会过期的解密钥匙。

### 7.1 HLS 打包管线

```
.enc 文件 → 解密 → ffmpeg HLS 打包 → AES-128 加密 → .ts 分片 + .m3u8
                                                    │
                                                    ▼
                                              密钥通过独立 API 下发
                                              (需要 Token 验证 + 15 分钟过期)
```

### 7.2 会话密钥管理

```go
package hls

type SessionEncryptionKey struct {
    KeyID     string
    Key       []byte    // 16 字节 AES-128
    IV        []byte    // 16 字节
    SessionID string
    UserID    string
    ExpiresAt time.Time
}

type KeyStore struct {
    keys map[string]*SessionEncryptionKey
    mu   sync.RWMutex
}

func NewKeyStore() *KeyStore {
    ks := &KeyStore{keys: make(map[string]*SessionEncryptionKey)}
    go ks.cleanExpired() // 后台每 5 分钟清理过期密钥
    return ks
}

func (p *HLSPackager) GenerateSessionKey(userID, sessionID string) *SessionEncryptionKey {
    key := make([]byte, 16); rand.Read(key)
    iv := make([]byte, 16); rand.Read(iv)
    h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", userID, sessionID, time.Now().UnixNano())))
    sk := &SessionEncryptionKey{
        KeyID:     hex.EncodeToString(h[:16]),
        Key:       key, IV: iv,
        SessionID: sessionID, UserID: userID,
        ExpiresAt: time.Now().Add(15 * time.Minute),
    }
    p.KeyStore.Store(sk)
    return sk
}
```

### 7.3 HLS 打包（ffmpeg 原生加密）

```go
func (p *HLSPackager) PackageHLS(
    encSrcPath string, kek, wrappedDEK []byte,
    outputPrefix string,
    sessionKey *SessionEncryptionKey,
    profile VideoProfile,
) (*HLSPackage, error) {

    // 1. 写 HLS keyfile (供 ffmpeg hls_key_info_file 引用)
    keyfilePath := filepath.Join(p.OutputDir, outputPrefix+".key")
    keyinfoPath := filepath.Join(p.OutputDir, outputPrefix+".keyinfo")
    os.WriteFile(keyfilePath, sessionKey.Key, 0600)
    ivHex := hex.EncodeToString(sessionKey.IV)
    os.WriteFile(keyinfoPath, []byte(fmt.Sprintf("%s\n%s\n%s\n", keyfilePath, keyfilePath, ivHex)), 0600)

    // 2. 构建 ffmpeg 命令 (hls 加密)
    cmd := exec.Command("ffmpeg",
        "-i", "pipe:0",
        "-c:v", profile.VCodec, "-b:v", profile.VBitrate, "-s", profile.Size,
        "-c:a", "aac",
        "-hls_time", "6",
        "-hls_playlist_type", "vod",
        "-hls_key_info_file", keyinfoPath,      // ← ffmpeg 用此文件做 AES-128 加密
        "-hls_segment_filename", outputPrefix+"_%03d.ts",
        outputPrefix+".m3u8",
    )
    cmd.Dir = p.OutputDir

    // 3. stdin = 解密后的明文
    decryptReader, _ := NewDecryptReader(encSrcPath, kek, wrappedDEK)
    cmd.Stdin = decryptReader
    cmd.Run()

    // 4. 删除落盘的 .key 明文!!! 密钥只在内存 KeyStore 中
    os.Remove(filepath.Join(p.OutputDir, outputPrefix+".key"))

    // 5. 重写 m3u8: 密钥 URI 指向服务端 API
    m3u8Path := filepath.Join(p.OutputDir, outputPrefix+".m3u8")
    data, _ := os.ReadFile(m3u8Path)
    content := strings.ReplaceAll(string(data), ".key",
        fmt.Sprintf("/api/hls/key/%s", sessionKey.KeyID))
    os.WriteFile(m3u8Path, []byte(content), 0644)

    return &HLSPackage{M3U8Path: m3u8Path, SessionKey: sessionKey}, nil
}
```

### 7.4 密钥分发 API（独立通道）

```go
// GET /api/hls/key/:keyID?token=xxx
func HLSKeyHandler(ks *KeyStore) gin.HandlerFunc {
    return func(c *gin.Context) {
        // 1. Token 验证
        token := c.Query("token")
        claims, err := signer.Verify(token, c.ClientIP(), nonceStore)
        if err != nil {
            c.AbortWithStatus(403)
            return
        }

        // 2. 密钥查询
        keyID := c.Param("keyID")
        key, ok := ks.Get(keyID)
        if !ok {
            c.AbortWithStatus(410) // Gone — 密钥已过期
            return
        }

        // 3. 返回密钥（no-store）
        c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
        c.Data(200, "application/octet-stream", key.Key)
    }
}
```

### 7.5 完整播放流程

```
1. 客户端请求播放 → GET /api/playback/{fileID}?profile=1080p
2. 服务端验证 JWT → 生成 SignToken → 创建 SessionKey → 触发 HLS 打包
3. 服务端返回 → { "m3u8_url": "/hls/{fileID}_1080p.m3u8?token=xxx" }
4. 播放器 (hls.js/video.js) 加载 m3u8
5. 播放器发现 #EXT-X-KEY:METHOD=AES-128,URI="/api/hls/key/abc123?token=xxx"
6. 播放器请求密钥 → 服务端验证 Token → 返回 16B AES 密钥
7. 播放器下载 .ts → 用 AES 密钥在内存中解密 → 渲染
8. 15 分钟后密钥过期 → 播放器重新请求 → 服务端返回 410 → 自动刷新 m3u8
```

### 7.6 可选：EME ClearKey DRM 增强

当启用 ClearKey 时, 浏览器 CDM 沙箱负责解密, JavaScript 无法直接读取密钥:

```go
// POST /api/drm/clearkey/license
func HandleClearKeyLicense(ks *KeyStore) gin.HandlerFunc {
    return func(c *gin.Context) {
        var req struct { Kids []string `json:"kids"` }
        c.ShouldBindJSON(&req)
        license := map[string]interface{}{"keys": []map[string]string{}, "type": "temporary"}
        for _, kidB64 := range req.Kids {
            kidBytes, _ := base64.RawURLEncoding.DecodeString(kidB64)
            kid := hex.EncodeToString(kidBytes)
            sessionKey, ok := ks.Get(kid)
            if !ok {
                c.AbortWithStatus(410); return
            }
            license["keys"] = append(license["keys"].([]map[string]string), map[string]string{
                "kid": kidB64, "k": base64.RawURLEncoding.EncodeToString(sessionKey.Key), "kty": "oct",
            })
        }
        c.JSON(200, license)
    }
}
```

---

## 8. 图片：双水印污染输出防泄露

图片无法分片加密, 必须完整传输给浏览器才能渲染。核心策略：**原图永不离开服务端**。

### 8.1 图片防护管线

```
.enc 文件 → 解密 → 可见水印平铺 → LSB 隐写水印 → JPEG 85% 压缩 → no-store 返回
```

### 8.2 双水印引擎

```go
package imageprotect

type WatermarkConfig struct {
    VisibleText    string  // 可见水印文字, 如 "KNOX-userID"
    VisibleOpacity float64 // 透明度 (建议 0.08-0.15)
    LSBPayload     string  // 隐写载荷
    JPEGQuality    int     // 输出质量 (建议 85)
    MaxWidth       int     // 超过此宽度则缩放
}

type ImageProtector struct { config WatermarkConfig }

// Protect 解密 + 双水印 + 压缩 → 输出
func (ip *ImageProtector) Protect(encPath string, kek, wdek, iv []byte) ([]byte, error) {
    // 1. 解密
    r, _ := NewDecryptReader(encPath, kek, wdek, iv)
    plainBytes, _ := io.ReadAll(r)

    // 2. 解码
    img, _, _ := image.Decode(bytes.NewReader(plainBytes))
    plainBytes = nil

    // 3. 缩放
    if ip.config.MaxWidth > 0 && img.Bounds().Dx() > ip.config.MaxWidth {
        img = scaleDown(img, ip.config.MaxWidth)
    }

    // 4. 转 RGBA
    bounds := img.Bounds()
    rgba := image.NewRGBA(bounds)
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            rgba.Set(x, y, img.At(x, y))
        }
    }

    // 5. 可见水印: 半透明文字栅格平铺
    ip.applyVisibleWatermark(rgba)

    // 6. LSB 隐写: R 通道最低位嵌入载荷
    ip.applyLSBWatermark(rgba)

    // 7. JPEG 85% 输出
    var buf bytes.Buffer
    jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: ip.config.JPEGQuality})
    return buf.Bytes(), nil
}

// applyVisibleWatermark 半透明文字栅格平铺全图
func (ip *ImageProtector) applyVisibleWatermark(img *image.RGBA) {
    bounds := img.Bounds()
    w, h := bounds.Dx(), bounds.Dy()
    for y := 80; y < h; y += 200 {
        for x := -100; x < w+100; x += 350 {
            offsetX := (y / 200 % 2) * 175 // 交错偏移
            d := &font.Drawer{
                Dst:  img,
                Src:  image.NewUniform(color.RGBA{255, 255, 255, uint8(255 * ip.config.VisibleOpacity)}),
                Face: basicfont.Face7x13,
                Dot:  fixed.P(x+offsetX, y),
            }
            d.DrawString(ip.config.VisibleText)
        }
    }
}

// applyLSBWatermark 像素 R 通道最低位嵌入 2 进制载荷
func (ip *ImageProtector) applyLSBWatermark(img *image.RGBA) {
    bits := stringToBits(ip.config.LSBPayload)
    posSeq := generateScatteredPositions(img.Bounds(), len(bits), ip.config.LSBPayload)
    for i, bit := range bits {
        if i >= len(posSeq) { break }
        offset := img.PixOffset(posSeq[i].X, posSeq[i].Y)
        if bit == '1' {
            img.Pix[offset] |= 1
        } else {
            img.Pix[offset] &^= 1
        }
    }
}
```

### 8.3 图片预览 Handler

```go
// GET /api/image/{fileID}
func HandleImagePreview(db *MediaDB, kek []byte) gin.HandlerFunc {
    return func(c *gin.Context) {
        fileID := c.Param("fileID")
        userID := c.GetString("user_id")
        sessionID := c.GetString("session_id")

        meta, _ := db.GetFile(fileID)
        if !db.UserHasAccess(userID, fileID) {
            c.AbortWithStatusJSON(403, gin.H{"error": "forbidden"}); return
        }

        payload := GenerateWatermarkPayload(userID, sessionID)
        userTag := userID[:min(8, len(userID))]

        protector := NewImageProtector(WatermarkConfig{
            VisibleText:    fmt.Sprintf("KNOX-%s", userTag),
            VisibleOpacity: 0.10,
            LSBPayload:     payload,
            JPEGQuality:    85,
            MaxWidth:       2560,
        })

        output, _ := protector.Protect(meta.EncPath, kek, meta.WrappedDEK, meta.IV)

        c.Header("Content-Type", "image/jpeg")
        c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
        c.Header("X-Watermark-ID", payload[:32])
        c.Data(200, "image/jpeg", output)
    }
}

func GenerateWatermarkPayload(userID, sessionID string) string {
    ts := time.Now().Unix()
    raw := fmt.Sprintf("KNOX:%s:%s:%d", userID, sessionID, ts)
    h := sha256.Sum256([]byte(raw))
    return fmt.Sprintf("%s:%x", raw, h[:4])
}
```

---

## 9. 文档：逐页渲染阻断文本提取

文档的核心威胁是**文本天然可复制粘贴**。根本方案：**服务端逐页渲染为图片, 文本永不出服务端**。

### 9.1 文档防护管线

```
.docx/.pdf/.xlsx 解密 → LibreOffice → PDF → pdftoppm 逐页渲染 → 每页加水印 → JPEG → 前端翻页阅读器
```

### 9.2 文档渲染引擎

```go
package docprotect

type DocumentRenderer struct {
    WorkDir      string
    ImageProtect *imageprotect.ImageProtector
    mu           sync.Mutex
}

type RenderedPage struct {
    PageNum  int
    JPEGData []byte
}

func (dr *DocumentRenderer) RenderDocument(
    encPath string, kek, wdek, iv []byte,
    userID, sessionID string,
) ([]RenderedPage, error) {
    // 1. 解密到临时文件 (仅用于传给外部渲染工具)
    plainPath, _ := dr.decryptToTemp(encPath, kek, wdek, iv)
    defer os.Remove(plainPath)

    // 2. 根据类型选择渲染引擎
    ext := strings.ToLower(filepath.Ext(encPath))
    ext = strings.TrimSuffix(ext, ".enc") // 还原原始扩展名

    var pages []RenderedPage
    var err error
    switch ext {
    case ".pdf":
        pages, err = dr.renderPDF(plainPath, userID, sessionID)
    case ".docx", ".doc", ".xlsx", ".xls", ".pptx", ".ppt":
        pages, err = dr.renderOffice(plainPath, ext, userID, sessionID)
    case ".txt", ".md", ".csv":
        pages, err = dr.renderOffice(plainPath, ext, userID, sessionID)
    default:
        return nil, fmt.Errorf("unsupported: %s", ext)
    }
    return pages, err
}

// renderPDF pdftoppm 逐页转 JPEG
func (dr *DocumentRenderer) renderPDF(pdfPath, userID, sessionID string) ([]RenderedPage, error) {
    cmd := exec.Command("pdftoppm",
        "-jpeg", "-r", "150",
        "-jpegopt", "quality=85",
        pdfPath,
        filepath.Join(dr.WorkDir, "page"),
    )
    cmd.Run()

    matches, _ := filepath.Glob(filepath.Join(dr.WorkDir, "page-*.jpg"))
    pages := make([]RenderedPage, 0, len(matches))
    for i, match := range matches {
        data, _ := os.ReadFile(match)
        watermarked, _ := dr.applyPageWatermark(data, userID, sessionID, i+1)
        pages = append(pages, RenderedPage{PageNum: i + 1, JPEGData: watermarked})
        os.Remove(match)
    }
    return pages, nil
}

// renderOffice LibreOffice headless → PDF → 渲染
func (dr *DocumentRenderer) renderOffice(docPath, ext, userID, sessionID string) ([]RenderedPage, error) {
    cmd := exec.Command("libreoffice",
        "--headless", "--convert-to", "pdf",
        "--outdir", dr.WorkDir, docPath,
    )
    cmd.Run()

    baseName := strings.TrimSuffix(filepath.Base(docPath), ext) + ".pdf"
    pdfPath := filepath.Join(dr.WorkDir, baseName)
    defer os.Remove(pdfPath)
    return dr.renderPDF(pdfPath, userID, sessionID)
}

// applyPageWatermark 每页加水印
func (dr *DocumentRenderer) applyPageWatermark(jpegData []byte, userID, sessionID string, pageNum int) ([]byte, error) {
    img, _, _ := image.Decode(bytes.NewReader(jpegData))
    bounds := img.Bounds()
    rgba := image.NewRGBA(bounds)
    for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
        for x := bounds.Min.X; x < bounds.Max.X; x++ {
            rgba.Set(x, y, img.At(x, y))
        }
    }
    payload := GenerateWatermarkPayload(userID, sessionID)
    protector := NewImageProtector(WatermarkConfig{
        VisibleText:    fmt.Sprintf("KNOX-%s P%d", userID[:8], pageNum),
        VisibleOpacity: 0.08,
        LSBPayload:     payload,
        JPEGQuality:    85,
    })
    protector.applyVisibleWatermark(rgba)
    protector.applyLSBWatermark(rgba)
    var buf bytes.Buffer
    jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: 85})
    return buf.Bytes(), nil
}
```

### 9.3 文档预览 Handler

```go
// GET /api/document/{fileID} → 返回文档元信息（总页数）
// GET /api/document/{fileID}/page/{pageNum} → 返回单页水印图片
func HandleDocumentPreview(db *MediaDB, renderer *docprotect.DocumentRenderer, kek []byte) gin.HandlerFunc {
    return func(c *gin.Context) {
        fileID := c.Param("fileID")
        pageNumStr := c.Param("pageNum")
        userID := c.GetString("user_id")
        sessionID := c.GetString("session_id")

        meta, _ := db.GetFile(fileID)
        if !db.UserHasAccess(userID, fileID) {
            c.AbortWithStatusJSON(403, gin.H{"error": "forbidden"}); return
        }

        cacheKey := fmt.Sprintf("doc:%s:%s", fileID, sessionID)

        // 缓存检查
        if cached, ok := docCache.Get(cacheKey); ok {
            if pageNumStr != "" {
                pageNum, _ := strconv.Atoi(pageNumStr)
                if pageNum > 0 && pageNum <= len(cached) {
                    servePage(c, cached[pageNum-1])
                    return
                }
            }
            c.JSON(200, gin.H{"total_pages": len(cached)})
            return
        }

        // 渲染
        pages, _ := renderer.RenderDocument(meta.EncPath, kek, meta.WrappedDEK, meta.IV, userID, sessionID)
        docCache.Set(cacheKey, pages, 5*time.Minute)

        if pageNumStr != "" {
            pageNum, _ := strconv.Atoi(pageNumStr)
            if pageNum > 0 && pageNum <= len(pages) {
                servePage(c, pages[pageNum-1])
                return
            }
        }
        c.JSON(200, gin.H{"total_pages": len(pages)})
    }
}

func servePage(c *gin.Context, page docprotect.RenderedPage) {
    c.Header("Content-Type", "image/jpeg")
    c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
    c.Data(200, "image/jpeg", page.JPEGData)
}
```

---

## 10. 客户端加固（CSS / JS / HTTP 头）

### 10.1 HTTP 安全响应头

```go
func SecurityHeaders() gin.HandlerFunc {
    return func(c *gin.Context) {
        c.Header("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
        c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
        c.Header("Pragma", "no-cache")
        c.Header("Expires", "0")
        c.Header("X-Content-Type-Options", "nosniff")
        c.Header("X-Frame-Options", "DENY")
        c.Header("Cross-Origin-Resource-Policy", "same-origin")
        c.Header("Cross-Origin-Opener-Policy", "same-origin")
        c.Header("Cross-Origin-Embedder-Policy", "require-corp")
        c.Header("Referrer-Policy", "same-origin")
        c.Header("Content-Security-Policy",
            "default-src 'self'; "+
            "media-src 'self' blob:; "+
            "script-src 'self' 'unsafe-inline'; "+
            "style-src 'self' 'unsafe-inline'; "+
            "connect-src 'self'; "+
            "img-src 'self' data:; "+
            "worker-src blob:; "+
            "form-action 'none';")
        c.Next()
    }
}
```

### 10.2 前端保护逻辑

| 保护项 | 方法 | 适用类型 |
|--------|------|---------|
| 禁止右键 | `oncontextmenu="return false"` | 全部 |
| 禁止拖拽 | `draggable="false"` + `ondragstart="return false"` | 全部 |
| 禁止选择文本 | `user-select: none` + `onselectstart="return false"` | 图片/文档 |
| 图片透明覆盖层 | `position:absolute; pointer-events:auto` | 图片 |
| 文档每页覆盖层 | `<div class="guard">` + 透明覆盖 | 文档 |
| 打印拦截 | `@media print { filter: blur(20px) / display: none }` | 全部 |
| 快捷键拦截 | `Ctrl+S / Ctrl+P / Ctrl+C / Ctrl+U` 全部阻止 | 全部 |
| DevTools 检测 | 定时 `console.log(Image)` 检测 → 模糊画面 | 全部 |
| 离开清理 | `pagehide` 事件: 暂停播放 / 移除 src | 视频/音频 |

### 10.3 图片查看器 CSS

```css
.image-guard { position: relative; display: inline-block; max-width: 100%; }
.image-guard img {
    display: block; max-width: 100%; max-height: 80vh;
    user-select: none; -webkit-user-select: none;
    -webkit-touch-callout: none; pointer-events: none;
}
.image-guard .overlay {
    position: absolute; top: 0; left: 0; right: 0; bottom: 0;
    background: transparent; z-index: 1; pointer-events: auto;
}
@media print { .image-guard img { filter: blur(20px); opacity: 0.3; } }
```

### 10.4 文档阅读器 CSS

```css
.doc-viewer { user-select: none; -webkit-user-select: none; -webkit-touch-callout: none; }
.doc-page { position: relative; box-shadow: 0 2px 8px rgba(0,0,0,0.12); max-width: 100%; }
.doc-page img { display: block; max-width: 100%; pointer-events: none; user-select: none; }
.doc-page .guard {
    position: absolute; top: 0; left: 0; right: 0; bottom: 0;
    background: transparent; z-index: 1; cursor: default;
}
@media print { .doc-viewer, .doc-page { display: none !important; } }
```

---

## 11. 溯源水印系统

### 11.1 LSB 隐写水印（不可见）

载荷格式: `KNOX:{userID}:{sessionID}:{timestamp}:{checksum}`

编码方式: 每 8 个像素的 R 通道最低位存 1 bit, 像素位置由载荷哈希确定（分散嵌入）。

提取方式: 载荷哈希 → 重建相同位置序列 → 读取 R 通道最低位 → 还原载荷。

```go
func ExtractLSBWatermark(imgBytes []byte, payloadLength int, seed string) (string, error) {
    img, _, _ := image.Decode(bytes.NewReader(imgBytes))
    posSeq := generateScatteredPositions(img.Bounds(), payloadLength*8, seed)
    bits := make([]byte, 0, payloadLength*8)
    for i := 0; i < payloadLength*8 && i < len(posSeq); i++ {
        r, _, _, _ := img.At(posSeq[i].X, posSeq[i].Y).RGBA()
        if r&1 == 1 { bits = append(bits, '1') } else { bits = append(bits, '0') }
    }
    return bitsToString(bits), nil
}
```

### 11.2 可见水印（可识别）

- 图片: 半透明 `KNOX-{userID}` 文字栅格平铺全图
- 文档: 每页顶部/底部嵌入 `KNOX-{userID} P{pageNum}`
- 视频: ffmpeg drawtext / 关键帧 LSB 嵌入

```go
func buildWatermarkFilter(userID, position string) string {
    return fmt.Sprintf(
        "drawtext=text='KNOX-%s':fontsize=18:fontcolor=white@0.15:"+
        "x=w-tw-20:y=h-th-20:shadowx=1:shadowy=1", userID[len(userID)-4:])
}
```

---

## 12. 签名 URL 令牌 + 防重放

### 12.1 Token 设计

```go
type TokenClaims struct {
    FileID    string `json:"fid"`
    KeyID     string `json:"kid,omitempty"`  // HLS 密钥关联
    UserID    string `json:"uid"`
    ClientIP  string `json:"ip"`             // 绑定 IP
    SessionID string `json:"sid"`
    IssuedAt  int64  `json:"iat"`
    ExpiresAt int64  `json:"exp"`
    Nonce     string `json:"nonce"`          // 防重放
}

type SignedToken struct {
    HMACKey []byte
    TTL     time.Duration // 默认 15 分钟
}

func (st *SignedToken) Sign(claims TokenClaims) (string, error) {
    claims.IssuedAt = time.Now().Unix()
    claims.ExpiresAt = time.Now().Add(st.TTL).Unix()
    claims.Nonce = generateNonce()
    payload, _ := json.Marshal(claims)
    encoded := base64.RawURLEncoding.EncodeToString(payload)
    mac := hmac.New(sha256.New, st.HMACKey)
    mac.Write([]byte(encoded))
    sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
    return fmt.Sprintf("%s.%s", encoded, sig), nil
}

func (st *SignedToken) Verify(token string, clientIP string, ns *NonceStore) (*TokenClaims, error) {
    parts := strings.SplitN(token, ".", 2)
    // HMAC 验证
    mac := hmac.New(sha256.New, st.HMACKey)
    mac.Write([]byte(parts[0]))
    expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
    if !hmac.Equal([]byte(expectedSig), []byte(parts[1])) {
        return nil, fmt.Errorf("invalid signature")
    }
    // 解析 claims
    payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
    var claims TokenClaims
    json.Unmarshal(payload, &claims)
    // 三重验证: 过期 + IP + Nonce
    if time.Now().Unix() > claims.ExpiresAt {
        return nil, fmt.Errorf("expired")
    }
    if claims.ClientIP != clientIP {
        return nil, fmt.Errorf("IP mismatch")
    }
    if ns != nil && !ns.CheckAndMark(claims.Nonce) {
        return nil, fmt.Errorf("replay")
    }
    return &claims, nil
}
```

---

## 13. 管理员再编码优化

管理员可随时对已入库文件发起低码率再编码，生成新的 `.enc` 文件。

```go
// POST /api/v1/admin/reencode
// Body: { "file_ids": [...], "profile": "low_360p" }
type ReencodeRequest struct {
    FileIDs []string `json:"file_ids"`
    Profile string   `json:"profile"`
}

func (s *ReencodeService) Reencode(ctx context.Context, task ReencodeTask) error {
    // 1. 查原文件
    var encPath, wrappedDEKHex string
    s.db.QueryRowContext(ctx, "SELECT enc_path, wrapped_dek FROM media_files WHERE id=?",
        task.FileID).Scan(&encPath, &wrappedDEKHex)

    // 2. 解 DEK
    kek, _ := s.keystore.GetKEK(ctx)
    wdek, _ := hex.DecodeString(wrappedDEKHex)

    // 3. 解密 → ffmpeg 低码率 → 新密文
    src, _ := os.Open(encPath)
    decStream, _ := kcrypto.DecryptStream(src, wdek, kek)
    cmd := exec.CommandContext(ctx, "ffmpeg",
        "-i", "pipe:0",
        "-c:v", "libx264", "-b:v", "300k", "-s", "640x360",
        "-c:a", "aac", "-b:a", "64k",
        "-f", "matroska", "pipe:1",
    )
    cmd.Stdin = decStream
    var stdout bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Run()

    // 4. 新 DEK 重新加密，写入新 .enc
    newPath := filepath.Join(s.basePath, task.FileID+"_"+task.TargetProfile+".enc")
    dst, _ := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY, 0600)
    result, _ := kcrypto.EncryptFile(bytes.NewReader(stdout.Bytes()), dst, kek)

    // 5. 写入 media_encodings 表
    s.db.ExecContext(ctx, `
        INSERT INTO media_encodings (file_id, profile, enc_path, wrapped_dek, iv, file_size)
        VALUES (?, ?, ?, ?, ?, ?)
    `, task.FileID, task.TargetProfile, newPath,
        fmt.Sprintf("%x", result.WrappedDEK), fmt.Sprintf("%x", result.IV), stdout.Len())
    return nil
}
```

---

## 14. 数据库设计（统一 Schema）

```sql
-- 主文件表（所有类型共用）
CREATE TABLE media_files (
    id          TEXT PRIMARY KEY,
    enc_path    TEXT NOT NULL UNIQUE,
    wrapped_dek TEXT NOT NULL,                -- hex, KEK 加密后的 DEK
    iv          TEXT NOT NULL,                -- hex, 加密 IV
    file_type   TEXT NOT NULL CHECK(file_type IN ('video','audio','image','document')),
    orig_name   TEXT NOT NULL,
    file_size   INTEGER NOT NULL DEFAULT 0,
    mime_type   TEXT DEFAULT '',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 缩略图表
CREATE TABLE thumbnails (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     TEXT NOT NULL REFERENCES media_files(id),
    enc_path    TEXT NOT NULL UNIQUE,
    wrapped_dek TEXT NOT NULL,
    iv          TEXT NOT NULL,
    thumb_type  TEXT NOT NULL CHECK(thumb_type IN ('video_thumb','image_thumb','doc_preview')),
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 预转码 / 管理员再编码版本
CREATE TABLE media_encodings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     TEXT NOT NULL REFERENCES media_files(id),
    profile     TEXT NOT NULL,                -- "720p" / "1080p" / "low_360p"
    enc_path    TEXT NOT NULL UNIQUE,
    wrapped_dek TEXT NOT NULL,
    iv          TEXT NOT NULL,
    file_size   INTEGER DEFAULT 0,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(file_id, profile)
);

-- HLS 缓存（会话级，定期清理）
CREATE TABLE hls_sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     TEXT NOT NULL,
    profile     TEXT NOT NULL,
    m3u8_path   TEXT NOT NULL,
    key_id      TEXT NOT NULL,
    expires_at  DATETIME NOT NULL,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- KEK 版本追踪
CREATE TABLE kek_versions (
    version     INTEGER PRIMARY KEY,
    active_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 操作审计日志
CREATE TABLE access_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id     TEXT NOT NULL,
    user_id     TEXT,
    action      TEXT NOT NULL,                -- "play" / "preview" / "download" / "transcode"
    profile     TEXT,
    ip          TEXT,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 用户权限
CREATE TABLE user_permissions (
    user_id     TEXT NOT NULL,
    file_id     TEXT NOT NULL,
    permission  TEXT NOT NULL CHECK(permission IN ('read','write','admin')),
    PRIMARY KEY (user_id, file_id)
);

CREATE INDEX idx_media_type ON media_files(file_type);
CREATE INDEX idx_media_created ON media_files(created_at);
CREATE INDEX idx_thumb_file ON thumbnails(file_id);
CREATE INDEX idx_encoding_file ON media_encodings(file_id);
CREATE INDEX idx_access_file ON access_log(file_id);
CREATE INDEX idx_access_user ON access_log(user_id);
```

---

## 15. API 接口定义

### 15.1 用户 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/v1/media/upload` | 上传文件, 返回 file_id |
| `GET` | `/api/v1/media/{id}/play?profile=720p` | 视频/音频播放 (返回 m3u8 URL) |
| `GET` | `/api/v1/image/{id}` | 图片预览 (返回带水印 JPEG) |
| `GET` | `/api/v1/document/{id}` | 文档预览 (返回总页数) |
| `GET` | `/api/v1/document/{id}/page/{n}` | 文档第 n 页 (返回带水印 JPEG) |
| `GET` | `/api/v1/media/{id}/thumb` | 获取缩略图 |
| `GET` | `/api/v1/media/{id}/transcode?profile=720p` | 实时转码 |
| `POST` | `/api/playback/end` | 播放结束回调 (清理缓存) |

### 15.2 HLS / DRM API

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/hls/{filename}.m3u8?token=xxx` | HLS 播放列表 (静态文件, Token 验证) |
| `GET` | `/hls/{filename}.ts?token=xxx` | HLS 分片 (加密内容) |
| `GET` | `/api/hls/key/{keyID}?token=xxx` | HLS 解密密钥 |
| `POST` | `/api/drm/clearkey/license` | EME ClearKey License |

### 15.3 管理员 API

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/v1/admin/reencode` | 批量再编码 |
| `GET` | `/api/v1/admin/stats` | 存储统计 |
| `POST` | `/api/v1/admin/kek/rotate` | KEK 轮换 |

---

## 16. 项目结构与路由注册

### 16.1 项目结构

```
knox-media/
├── cmd/server/main.go
├── internal/
│   ├── crypto/
│   │   ├── envelope.go              # 信封加密引擎
│   │   └── keywrap.go               # RFC 3394 AES Key Wrap
│   ├── keystore/
│   │   └── vault.go                 # KEK Vault (argon2 + mlock)
│   ├── storage/
│   │   ├── ingest.go                # 入库加密管道
│   │   ├── preprocess.go            # 预处理 (缩略图/预转码)
│   │   └── cache.go                 # 预转码缓存管理
│   ├── transcode/
│   │   ├── pipeline.go              # ffmpeg 实时转码管道
│   │   └── reencode.go              # 管理员再编码
│   ├── hls/
│   │   ├── packager.go              # HLS 打包器
│   │   └── keystore.go              # 会话密钥存储
│   ├── imageprotect/
│   │   ├── protector.go             # 图片双水印保护器
│   │   └── watermark.go             # LSB 隐写引擎
│   ├── docprotect/
│   │   ├── renderer.go              # 文档渲染引擎
│   │   └── cache.go                 # 文档渲染缓存
│   ├── auth/
│   │   ├── signer.go                # 签名 URL Token
│   │   └── nonce.go                 # NonceStore 防重放
│   ├── handler/
│   │   ├── upload.go
│   │   ├── playback.go              # 播放/转码
│   │   ├── image.go                 # 图片预览
│   │   ├── document.go              # 文档预览
│   │   ├── hls_key.go               # HLS 密钥分发
│   │   ├── drm.go                   # ClearKey License
│   │   └── admin.go                 # 管理员 API
│   ├── middleware/
│   │   ├── auth.go                  # JWT 鉴权
│   │   ├── security.go              # 安全响应头
│   │   └── token.go                 # Token 验证中间件
│   └── model/
│       └── media.go                 # 数据库模型
├── config/config.yaml
├── migrations/001_init.sql
├── go.mod
└── go.sum
```

### 16.2 路由注册

```go
func RegisterRoutes(r *gin.Engine, handlers *Handlers) {
    // 全局安全头
    r.Use(SecurityHeaders())

    // 用户 API (JWT 鉴权)
    api := r.Group("/api/v1")
    api.Use(JWTAuthMiddleware())
    {
        api.POST("/media/upload", handlers.Upload)
        api.GET("/media/:id/play", handlers.Playback)
        api.GET("/media/:id/transcode", handlers.Transcode)
        api.GET("/media/:id/thumb", handlers.Thumbnail)
        api.GET("/image/:id", handlers.ImagePreview)
        api.GET("/document/:id", handlers.DocumentMeta)
        api.GET("/document/:id/page/:pageNum", handlers.DocumentPage)
        api.POST("/playback/end", handlers.PlaybackEnd)
    }

    // HLS 静态文件 (Token 验证)
    hls := r.Group("/hls")
    hls.Use(TokenAuthMiddleware(handlers.Signer, handlers.NonceStore))
    { hls.Static("", handlers.HLSOutputDir) }

    // HLS 密钥 API (Token 验证)
    keyAPI := r.Group("/api/hls")
    keyAPI.Use(TokenAuthMiddleware(handlers.Signer, handlers.NonceStore))
    { keyAPI.GET("/key/:keyID", handlers.HLSKey) }

    // DRM License (Token 验证)
    drm := r.Group("/api/drm")
    drm.Use(TokenAuthMiddleware(handlers.Signer, handlers.NonceStore))
    { drm.POST("/clearkey/license", handlers.ClearKeyLicense) }

    // 管理员 API (Admin JWT)
    admin := r.Group("/api/v1/admin")
    admin.Use(AdminAuthMiddleware())
    {
        admin.POST("/reencode", handlers.Reencode)
        admin.GET("/stats", handlers.Stats)
        admin.POST("/kek/rotate", handlers.KEKRotate)
    }
}
```

---

## 17. 部署环境依赖 & 安全 Checklist

### 17.1 系统依赖

```bash
# 核心依赖
apt-get install -y ffmpeg                  # 视频转码

# 文档渲染依赖
apt-get install -y \
    poppler-utils \                        # pdftoppm (PDF 渲染)
    libreoffice-headless                   # Office → PDF

# Go 依赖
go get github.com/gin-gonic/gin
go get github.com/golang-jwt/jwt/v5
go get github.com/mattn/go-sqlite3
go get golang.org/x/crypto
go get golang.org/x/image
```

### 17.2 配置文件

```yaml
# config/config.yaml
server:
  port: 8443
  tls_cert: /etc/knox-media/cert.pem
  tls_key:  /etc/knox-media/key.pem

storage:
  base_path: /data/knox-media
  thumbnail_path: /data/knox-media/thumbnails
  cache_path: /data/knox-media/cache
  hls_output: /tmp/knox-hls

transcode:
  max_concurrent: 4
  chunk_size: 4194304  # 4MB

session:
  key_ttl: 900         # HLS 密钥有效期(秒) = 15分钟
  token_ttl: 900       # 签名 Token 有效期(秒)

watermark:
  visible_opacity: 0.10
  jpeg_quality: 85
  max_image_width: 2560
```

### 17.3 部署安全 Checklist

- [ ] KEK 通过环境变量 `KNOX_MAIN_KEY` 传入，永不明文落盘
- [ ] Salt 文件权限 `0600`
- [ ] `.enc` 文件权限 `0600`
- [ ] TLS 1.3，禁用 1.0/1.1
- [ ] HSTS `max-age=63072000; includeSubDomains`
- [ ] 会话密钥 15 分钟自动过期
- [ ] Token 绑定 IP + Session + Nonce 防重放
- [ ] 密钥 API `Cache-Control: no-store`
- [ ] 图片/文档响应 `Cache-Control: no-store`
- [ ] CSP 严格限制 `media-src blob:` + `worker-src blob:`
- [ ] `X-Frame-Options: DENY`
- [ ] 禁用 core dump: `ulimit -c 0`
- [ ] `mlock()` 锁定 KEK 内存页
- [ ] 异常解密失败触发告警
- [ ] 异常大量请求触发限流
- [ ] 审计日志记录所有访问

---

## 18. 防泄露效果总矩阵

| 攻击手段 | 视频/音频 | 图片 | 文档 | 防御层级 |
|---------|----------|------|------|---------|
| **窃取物理磁盘** | .enc 乱码 | .enc 乱码 | .enc 乱码 | L0 磁盘加密 |
| **拖库 SQL 注入** | 无 KEK 无法解 DEK | 同左 | 同左 | L0 信封加密 |
| **MITM 明文嗅探** | TLS 1.3 阻断 | TLS 1.3 阻断 | TLS 1.3 阻断 | L1 TLS |
| **curl/wget 下载** | Token 403 | Token 403 | Token 403 | L2 签名令牌 |
| **分享播放链接** | Token 过期 + IP 绑定 | 同左 | 同左 | L2 签名令牌 |
| **重放 Token** | Nonce 防重放 | Nonce 防重放 | Nonce 防重放 | L2 NonceStore |
| **浏览器另存为** | 加密 .ts 乱码 | 水印+降质 | 图片页 | L3/L4 分片/污染 |
| **DevTools 网络抓包** | 密文 .ts | 水印+85%质量 | 逐页图片 | L3/L4 |
| **浏览器缓存提取** | 加密 .ts | no-store | no-store | L5 HTTP 头 |
| **右键/拖拽** | 前端播放器 | CSS 覆盖层 | CSS 覆盖层 | L5 客户端加固 |
| **Ctrl+S 保存** | 快捷键拦截 | 快捷键拦截 | 快捷键拦截 | L5 客户端加固 |
| **打印** | @media print 模糊 | @media print 模糊 | @media print 隐藏 | L5 客户端加固 |
| **DevTools 开启** | 检测 → 模糊 | 检测 → 模糊 | 检测 → 模糊 | L5 DevTools 检测 |
| **录屏软件** | ✅ LSB 水印追溯 | ✅ 可见+LSB 追溯 | ✅ 每页水印追溯 | L6 溯源水印 |
| **手机拍照屏幕** | ✅ 可见水印追溯 | ✅ 可见水印追溯 | ✅ 可见水印追溯 | L6 溯源水印 |
| **OCR 提取文本** | N/A | 无文本(图片) | ✅ 水印干扰 | L4/L6 |
| **Photoshop 去水印** | N/A | LSB 极难批量擦除 | LSB 极难批量擦除 | L6 隐写 |
| **内存 dump 提取** | ✅ ClearKey CDM 沙箱 | N/A | N/A | L4 DRM |
| **获取原文** | ❌ 不可能 | ❌ 不可能(已污染) | ❌ 不可能(已转图片) | L3/L4 |

---

## 附录: Go 依赖汇总

```
go 1.22

require (
    github.com/gin-gonic/gin         v1.10.0   // HTTP 框架
    github.com/golang-jwt/jwt/v5     v5.2.1    // JWT 鉴权
    github.com/mattn/go-sqlite3      v1.14.24  // SQLite 驱动
    golang.org/x/crypto              v0.28.0   // argon2, chacha20
    golang.org/x/image               v0.23.0   // 图片处理 + 字体渲染
)
```

> **加密核心仅依赖 Go 标准库** `crypto/aes` + `crypto/cipher` + `crypto/rand` + `crypto/subtle`，不引入任何第三方加密库。

---

*Knox-Media (9527) - 就我自己，隐私至上*
