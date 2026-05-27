package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/photoface"
	"knox-media/internal/recognition"
)

type SystemOptionsPhotoFace struct {
	AutoOnScan          bool    `json:"auto_on_scan"`
	PythonPath          string  `json:"python_path"`
	ScriptPath          string  `json:"script_path"`
	SimilarityThreshold float64 `json:"similarity_threshold"`
}

type photoFaceTestBody struct {
	PhotoFace *SystemOptionsPhotoFace `json:"photo_face"`
}

type photoFaceInstallResult struct {
	OK        bool                    `json:"ok"`
	Message   string                  `json:"message"`
	PhotoFace *SystemOptionsPhotoFace `json:"photo_face,omitempty"`
}

func defaultPhotoFaceOptions() SystemOptionsPhotoFace {
	return SystemOptionsPhotoFace{
		AutoOnScan:          true,
		PythonPath:          "",
		ScriptPath:          "tools/photo_face/detect.py",
		SimilarityThreshold: 0.45,
	}
}

func photoFaceFromConfig(cfg *config.Config) SystemOptionsPhotoFace {
	def := defaultPhotoFaceOptions()
	if cfg == nil {
		return def
	}
	pf := cfg.PhotoFace
	out := SystemOptionsPhotoFace{
		AutoOnScan:          cfg.PhotoFaceAutoOnScan(),
		PythonPath:          strings.TrimSpace(pf.PythonPath),
		ScriptPath:          strings.TrimSpace(pf.ScriptPath),
		SimilarityThreshold: float64(cfg.PhotoFaceSimilarityThreshold()),
	}
	if out.ScriptPath == "" {
		out.ScriptPath = def.ScriptPath
	}
	if out.PythonPath == "" {
		out.PythonPath = strings.TrimSpace(cfg.PhotoClassify.PythonPath)
	}
	return normalizePhotoFaceOptions(out)
}

func photoFaceToConfig(o SystemOptionsPhotoFace) config.PhotoFaceConfig {
	o = normalizePhotoFaceOptions(o)
	autoOn := o.AutoOnScan
	th := float32(o.SimilarityThreshold)
	return config.PhotoFaceConfig{
		AutoOnScan:          &autoOn,
		PythonPath:          o.PythonPath,
		ScriptPath:          o.ScriptPath,
		SimilarityThreshold: th,
	}
}

func normalizePhotoFaceOptions(o SystemOptionsPhotoFace) SystemOptionsPhotoFace {
	o.PythonPath = strings.TrimSpace(o.PythonPath)
	o.ScriptPath = strings.TrimSpace(o.ScriptPath)
	def := defaultPhotoFaceOptions()
	if o.ScriptPath == "" {
		o.ScriptPath = def.ScriptPath
	}
	if o.SimilarityThreshold <= 0 || o.SimilarityThreshold > 1 {
		o.SimilarityThreshold = def.SimilarityThreshold
	}
	return o
}

func fillPhotoFaceDefaults(o *SystemOptionsPhotoFace, def SystemOptionsPhotoFace) {
	if o == nil {
		return
	}
	if strings.TrimSpace(o.ScriptPath) == "" {
		o.ScriptPath = def.ScriptPath
	}
	if o.SimilarityThreshold <= 0 {
		o.SimilarityThreshold = def.SimilarityThreshold
	}
}

func (h *Handler) applyPhotoFaceConfig(o SystemOptionsPhotoFace) error {
	if h == nil || h.App == nil || h.App.Config == nil {
		return fmt.Errorf("config unavailable")
	}
	pf := photoFaceToConfig(o)
	cfgPath := strings.TrimSpace(h.App.ConfigPath)
	if cfgPath == "" {
		return fmt.Errorf("config path not set")
	}
	if err := config.SavePhotoFace(cfgPath, pf); err != nil {
		return err
	}
	h.App.Config.PhotoFace = pf
	return nil
}

func (h *Handler) resolvePhotoFaceForTest(body *photoFaceTestBody) (string, config.PhotoFaceConfig) {
	mediaRoot := ""
	if h != nil && h.App != nil {
		if p := strings.TrimSpace(h.App.ConfigPath); p != "" {
			mediaRoot = recognition.MediaRoot(p)
		}
	}
	if body != nil && body.PhotoFace != nil {
		o := normalizePhotoFaceOptions(*body.PhotoFace)
		return mediaRoot, photoFaceToConfig(o)
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		return mediaRoot, h.App.Config.PhotoFace
	}
	return mediaRoot, photoFaceToConfig(defaultPhotoFaceOptions())
}

// TestSystemOptionsPhotoFace checks face detect engine connectivity.
func (h *Handler) TestSystemOptionsPhotoFace(c *gin.Context) {
	var body photoFaceTestBody
	_ = c.ShouldBindJSON(&body)
	mediaRoot, cfg := h.resolvePhotoFaceForTest(&body)
	result := photoface.CheckPhotoFaceConfig(c.Request.Context(), mediaRoot, cfg)
	c.JSON(http.StatusOK, result)
}

// InstallSystemOptionsPhotoFace installs InsightFace deps and writes config.yml.
func (h *Handler) InstallSystemOptionsPhotoFace(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Minute)
	defer cancel()

	deploy, err := photoface.InstallPhotoFace(ctx, mediaRoot)
	if err != nil {
		c.JSON(http.StatusOK, photoFaceInstallResult{OK: false, Message: err.Error()})
		return
	}
	current := photoFaceFromConfig(h.App.Config)
	current = faceDeployToOptions(deploy, current)
	if err := h.applyPhotoFaceConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := photoface.CheckPhotoFaceConfig(ctx, mediaRoot, photoface.DeployToConfig(deploy))
	msg := "人脸检测依赖已安装并写入 config.yml"
	if check.Message != "" && check.OK {
		msg = msg + "；" + check.Message
	} else if check.Message != "" {
		msg = check.Message
	}
	c.JSON(http.StatusOK, photoFaceInstallResult{
		OK:        check.OK,
		Message:   msg,
		PhotoFace: &current,
	})
}

func faceDeployToOptions(d photoface.FaceDeploy, base SystemOptionsPhotoFace) SystemOptionsPhotoFace {
	base.AutoOnScan = d.AutoOnScan
	if d.PythonPath != "" {
		base.PythonPath = d.PythonPath
	}
	if d.ScriptPath != "" {
		base.ScriptPath = d.ScriptPath
	}
	if d.SimilarityThreshold > 0 {
		base.SimilarityThreshold = float64(d.SimilarityThreshold)
	}
	return normalizePhotoFaceOptions(base)
}
