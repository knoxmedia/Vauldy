package handler

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

func (h *Handler) AdminOverview(c *gin.Context) {
	data, err := h.buildAdminOverviewData()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *Handler) AdminOverviewStream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Stream(func(w io.Writer) bool {
		data, err := h.buildAdminOverviewData()
		if err != nil {
			payload, _ := json.Marshal(gin.H{"error": err.Error()})
			_, _ = w.Write([]byte("event: error\n"))
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		} else {
			payload, _ := json.Marshal(data)
			_, _ = w.Write([]byte("event: overview\n"))
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
		}
		time.Sleep(3 * time.Second)
		return true
	})
}

func (h *Handler) buildAdminOverviewData() (gin.H, error) {
	cpuPercent := 0.0
	if vals, err := cpu.Percent(200*time.Millisecond, false); err == nil && len(vals) > 0 {
		cpuPercent = vals[0]
	}
	memPercent := 0.0
	memTotal := uint64(0)
	if vm, err := mem.VirtualMemory(); err == nil {
		memPercent = vm.UsedPercent
		memTotal = vm.Total
	}
	diskPercent := 0.0
	diskPath := h.App.Config.Data.Dir
	if diskPath == "" {
		diskPath = "."
	}
	if abs, err := filepath.Abs(diskPath); err == nil {
		diskPath = abs
	}
	if du, err := disk.Usage(diskPath); err == nil {
		diskPercent = du.UsedPercent
	}
	var transcodeTasks int64
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM transcode_task WHERE status IN ('waiting','running')`).Scan(&transcodeTasks)
	var mediaTotal int64
	_ = h.App.DB.QueryRow(`SELECT COUNT(1) FROM media`).Scan(&mediaTotal)

	var dbVersion sql.NullString
	_ = h.App.DB.QueryRow(`SELECT sqlite_version()`).Scan(&dbVersion)
	softwareVersion := "dev"
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			softwareVersion = bi.Main.Version
		}
	}
	rows, err := h.App.DB.Query(`
		SELECT id, COALESCE(username,''), action, COALESCE(media_id,0), COALESCE(message,''), created_at
		FROM activity_log
		ORDER BY id DESC
		LIMIT 30
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	activities := make([]gin.H, 0, 30)
	for rows.Next() {
		var id int64
		var username, action, message, createdAt sql.NullString
		var mediaID sql.NullInt64
		if err := rows.Scan(&id, &username, &action, &mediaID, &message, &createdAt); err != nil {
			continue
		}
		activities = append(activities, gin.H{
			"id":         id,
			"username":   username.String,
			"action":     action.String,
			"media_id":   nullInt64(mediaID),
			"message":    message.String,
			"created_at": createdAt.String,
		})
	}
	return gin.H{
		"monitor": gin.H{
			"cpu_percent":          cpuPercent,
			"memory_percent":       memPercent,
			"disk_percent":         diskPercent,
			"transcode_task_count": transcodeTasks,
			"media_total":          mediaTotal,
		},
		"system": gin.H{
			"cpu_count":        runtime.NumCPU(),
			"memory_total":     memTotal,
			"os":               runtime.GOOS + "/" + runtime.GOARCH,
			"database":         "sqlite " + dbVersion.String,
			"software_version": softwareVersion,
		},
		"activities": activities,
	}, nil
}
