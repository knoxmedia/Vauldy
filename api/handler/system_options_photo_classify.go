package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/config"
	"knox-media/internal/photoclass"
	"knox-media/internal/recognition"
)

type SystemOptionsPhotoClassify struct {
	AutoOnScan bool   `json:"auto_on_scan"`
	Engine     string `json:"engine"`
	PythonPath string `json:"python_path"`
	ScriptPath string `json:"script_path"`
	ModelPath  string `json:"model_path"`
	LabelsPath string `json:"labels_path"`
}

type photoClassifyTestBody struct {
	PhotoClassify *SystemOptionsPhotoClassify `json:"photo_classify"`
}

type photoClassifyInstallResult struct {
	OK            bool                        `json:"ok"`
	Message       string                      `json:"message"`
	PhotoClassify *SystemOptionsPhotoClassify `json:"photo_classify,omitempty"`
}

func defaultPhotoClassifyOptions() SystemOptionsPhotoClassify {
	autoOn := true
	_ = autoOn
	return SystemOptionsPhotoClassify{
		AutoOnScan: true,
		Engine:     "auto",
		PythonPath: "",
		ScriptPath: "tools/photo_classify/classify.py",
		ModelPath:  "tools/photo_classify/models/mobilenetv2-7.onnx",
		LabelsPath: "tools/photo_classify/imagenet_labels.txt",
	}
}

func photoClassifyFromConfig(cfg *config.Config) SystemOptionsPhotoClassify {
	def := defaultPhotoClassifyOptions()
	if cfg == nil {
		return def
	}
	pc := cfg.PhotoClassify
	out := SystemOptionsPhotoClassify{
		AutoOnScan: cfg.PhotoClassifyAutoOnScan(),
		Engine:     cfg.PhotoClassifyEngine(),
		PythonPath: strings.TrimSpace(pc.PythonPath),
		ScriptPath: strings.TrimSpace(pc.ScriptPath),
		ModelPath:  strings.TrimSpace(pc.ModelPath),
		LabelsPath: strings.TrimSpace(pc.LabelsPath),
	}
	if out.ScriptPath == "" {
		out.ScriptPath = def.ScriptPath
	}
	if out.ModelPath == "" {
		out.ModelPath = def.ModelPath
	}
	if out.LabelsPath == "" {
		out.LabelsPath = def.LabelsPath
	}
	return normalizePhotoClassifyOptions(out)
}

func photoClassifyToConfig(o SystemOptionsPhotoClassify) config.PhotoClassifyConfig {
	o = normalizePhotoClassifyOptions(o)
	autoOn := o.AutoOnScan
	return config.PhotoClassifyConfig{
		AutoOnScan: &autoOn,
		Engine:     o.Engine,
		PythonPath: o.PythonPath,
		ScriptPath: o.ScriptPath,
		ModelPath:  o.ModelPath,
		LabelsPath: o.LabelsPath,
	}
}

func normalizePhotoClassifyOptions(o SystemOptionsPhotoClassify) SystemOptionsPhotoClassify {
	switch strings.ToLower(strings.TrimSpace(o.Engine)) {
	case "auto", "heuristic", "onnx":
		o.Engine = strings.ToLower(strings.TrimSpace(o.Engine))
	default:
		o.Engine = "auto"
	}
	o.PythonPath = strings.TrimSpace(o.PythonPath)
	o.ScriptPath = strings.TrimSpace(o.ScriptPath)
	o.ModelPath = strings.TrimSpace(o.ModelPath)
	o.LabelsPath = strings.TrimSpace(o.LabelsPath)
	def := defaultPhotoClassifyOptions()
	if o.ScriptPath == "" {
		o.ScriptPath = def.ScriptPath
	}
	if o.ModelPath == "" {
		o.ModelPath = def.ModelPath
	}
	if o.LabelsPath == "" {
		o.LabelsPath = def.LabelsPath
	}
	return o
}

func fillPhotoClassifyDefaults(o *SystemOptionsPhotoClassify, def SystemOptionsPhotoClassify) {
	if o == nil {
		return
	}
	if strings.TrimSpace(o.Engine) == "" {
		o.Engine = def.Engine
	}
	if strings.TrimSpace(o.ScriptPath) == "" {
		o.ScriptPath = def.ScriptPath
	}
	if strings.TrimSpace(o.ModelPath) == "" {
		o.ModelPath = def.ModelPath
	}
	if strings.TrimSpace(o.LabelsPath) == "" {
		o.LabelsPath = def.LabelsPath
	}
}

func (h *Handler) applyPhotoClassifyConfig(o SystemOptionsPhotoClassify) error {
	if h == nil || h.App == nil || h.App.Config == nil {
		return fmt.Errorf("config unavailable")
	}
	pc := photoClassifyToConfig(o)
	cfgPath := strings.TrimSpace(h.App.ConfigPath)
	if cfgPath == "" {
		return fmt.Errorf("config path not set")
	}
	if err := config.SavePhotoClassify(cfgPath, pc); err != nil {
		return err
	}
	h.App.Config.PhotoClassify = pc
	return nil
}

func (h *Handler) resolvePhotoClassifyForTest(body *photoClassifyTestBody) (string, config.PhotoClassifyConfig) {
	mediaRoot := ""
	if h != nil && h.App != nil {
		if p := strings.TrimSpace(h.App.ConfigPath); p != "" {
			mediaRoot = recognition.MediaRoot(p)
		}
	}
	if body != nil && body.PhotoClassify != nil {
		o := normalizePhotoClassifyOptions(*body.PhotoClassify)
		return mediaRoot, photoClassifyToConfig(o)
	}
	if h != nil && h.App != nil && h.App.Config != nil {
		return mediaRoot, h.App.Config.PhotoClassify
	}
	return mediaRoot, photoClassifyToConfig(defaultPhotoClassifyOptions())
}

// TestSystemOptionsPhotoClassify checks photo classify engine connectivity.
func (h *Handler) TestSystemOptionsPhotoClassify(c *gin.Context) {
	var body photoClassifyTestBody
	_ = c.ShouldBindJSON(&body)
	mediaRoot, cfg := h.resolvePhotoClassifyForTest(&body)
	result := photoclass.CheckPhotoClassifyConfig(c.Request.Context(), mediaRoot, cfg)
	c.JSON(http.StatusOK, result)
}

// InstallSystemOptionsPhotoClassify installs Python deps, ONNX model, and writes config.yml.
func (h *Handler) InstallSystemOptionsPhotoClassify(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Minute)
	defer cancel()

	deploy, err := photoclass.InstallPhotoClassify(ctx, mediaRoot)
	if err != nil {
		c.JSON(http.StatusOK, photoClassifyInstallResult{OK: false, Message: err.Error()})
		return
	}
	current := photoClassifyFromConfig(h.App.Config)
	current = normalizePhotoClassifyOptions(deployToOptions(deploy))
	if err := h.applyPhotoClassifyConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := photoclass.CheckPhotoClassifyConfig(ctx, mediaRoot, photoclass.DeployToConfig(deploy))
	msg := "智能分类依赖已安装并写入 config.yml"
	if check.Message != "" {
		msg = msg + "；" + check.Message
	}
	c.JSON(http.StatusOK, photoClassifyInstallResult{
		OK:            check.OK,
		Message:       msg,
		PhotoClassify: &current,
	})
}

func deployToOptions(d photoclass.ClassifyDeploy) SystemOptionsPhotoClassify {
	return SystemOptionsPhotoClassify{
		AutoOnScan: d.AutoOnScan,
		Engine:     d.Engine,
		PythonPath: d.PythonPath,
		ScriptPath: d.ScriptPath,
		ModelPath:  d.ModelPath,
		LabelsPath: d.LabelsPath,
	}
}
