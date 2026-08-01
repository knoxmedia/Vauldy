package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"knox-media/internal/recognition"
	"knox-media/internal/subtitle"
)

type recognitionInstallResult struct {
	OK          bool                      `json:"ok"`
	Message     string                    `json:"message"`
	Recognition *SystemOptionsRecognition `json:"recognition,omitempty"`
}

type asrInstallBody struct {
	Engine   string `json:"engine"`
	Model    string `json:"model"`
	Language string `json:"language"`
	Device   string `json:"device"`
}

func (h *Handler) mediaRoot() (string, error) {
	if h == nil || h.App == nil {
		return "", fmt.Errorf("app unavailable")
	}
	p := strings.TrimSpace(h.App.ConfigPath)
	if p == "" {
		return "", fmt.Errorf("config path not set")
	}
	return recognition.MediaRoot(p), nil
}

func deployASRToOptions(d recognition.ASRDeploy) SystemOptionsASR {
	return SystemOptionsASR{
		Provider:    d.Provider,
		Engine:      d.Engine,
		Model:       d.Model,
		Language:    d.Language,
		Device:      d.Device,
		WhisperPath: d.WhisperPath,
		ExtraArgs:   append([]string(nil), d.ExtraArgs...),
		Shell:       d.Shell,
	}
}

func deployOCRToOptions(d recognition.OCRDeploy) SystemOptionsOCR {
	return SystemOptionsOCR{
		Enabled:        d.Enabled,
		TesseractPath:  d.TesseractPath,
		TessdataPrefix: d.TessdataPrefix,
		Languages:      d.Languages,
		PythonPath:     d.PythonPath,
		ScriptPath:     d.ScriptPath,
		PgsripPath:     d.PgsripPath,
	}
}

// InstallSystemOptionsASR downloads/installs ASR tools and writes config.yml.
func (h *Handler) InstallSystemOptionsASR(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var body asrInstallBody
	_ = c.ShouldBindJSON(&body)

	current := recognitionFromConfig(h.App.Config)
	opts := recognition.InstallASROptions{
		Engine:   firstNonEmpty(body.Engine, current.ASR.Engine, "faster-whisper"),
		Model:    firstNonEmpty(body.Model, current.ASR.Model, "base"),
		Language: firstNonEmpty(body.Language, current.ASR.Language, "zh"),
		Device:   firstNonEmpty(body.Device, current.ASR.Device),
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Minute)
	defer cancel()

	deploy, err := recognition.InstallASR(ctx, mediaRoot, opts)
	if err != nil {
		c.JSON(http.StatusOK, recognitionInstallResult{OK: false, Message: err.Error()})
		return
	}
	installed := deployASRToOptions(deploy)
	installed.AutoOnScan = current.ASR.AutoOnScan
	current.ASR = installed
	current = normalizeRecognitionOptions(current)
	if err := h.applyRecognitionConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := subtitle.CheckASRConfig(ctx, mediaRoot, subtitle.ASRConfig{
		Provider:    current.ASR.Provider,
		Engine:      current.ASR.Engine,
		Model:       current.ASR.Model,
		Language:    current.ASR.Language,
		Device:      current.ASR.Device,
		WhisperPath: current.ASR.WhisperPath,
		ExtraArgs:   append([]string(nil), current.ASR.ExtraArgs...),
		Shell:       current.ASR.Shell,
	})
	msg := fmt.Sprintf("ASR（%s / %s）已安装并写入 config.yml", current.ASR.Engine, current.ASR.Model)
	if check.Message != "" {
		msg = msg + "；" + check.Message
	}
	c.JSON(http.StatusOK, recognitionInstallResult{
		OK:          check.OK,
		Message:     msg,
		Recognition: &current,
	})
}

// InstallSystemOptionsOCR downloads/installs OCR tools and writes config.yml.
func (h *Handler) InstallSystemOptionsOCR(c *gin.Context) {
	mediaRoot, err := h.mediaRoot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Minute)
	defer cancel()

	deploy, err := recognition.InstallOCR(ctx, mediaRoot)
	if err != nil {
		c.JSON(http.StatusOK, recognitionInstallResult{OK: false, Message: err.Error()})
		return
	}
	current := recognitionFromConfig(h.App.Config)
	current.OCR = deployOCRToOptions(deploy)
	current = normalizeRecognitionOptions(current)
	if err := h.applyRecognitionConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := subtitle.CheckOCRConfig(ctx, mediaRoot, subtitle.OCRConfig{
		Enabled:        current.OCR.Enabled,
		TesseractPath:  current.OCR.TesseractPath,
		TessdataPrefix: current.OCR.TessdataPrefix,
		Languages:      current.OCR.Languages,
		PythonPath:     current.OCR.PythonPath,
		ScriptPath:     current.OCR.ScriptPath,
		PgsripPath:     current.OCR.PgsripPath,
		MkvextractPath: current.OCR.MkvextractPath,
		MkvmergePath:   current.OCR.MkvmergePath,
	})
	msg := "OCR 工具已安装并写入 config.yml"
	if check.Message != "" {
		msg = msg + "；" + check.Message
	}
	c.JSON(http.StatusOK, recognitionInstallResult{
		OK:          check.OK,
		Message:     msg,
		Recognition: &current,
	})
}
