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
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Minute)
	defer cancel()

	deploy, err := recognition.InstallASR(ctx, mediaRoot)
	if err != nil {
		c.JSON(http.StatusOK, recognitionInstallResult{OK: false, Message: err.Error()})
		return
	}
	current := recognitionFromConfig(h.App.Config)
	current.ASR = deployASRToOptions(deploy)
	current = normalizeRecognitionOptions(current)
	if err := h.applyRecognitionConfig(current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "写入 config.yml 失败: " + err.Error()})
		return
	}
	check := subtitle.CheckASRConfig(ctx, subtitle.ASRConfig{
		Provider:    current.ASR.Provider,
		WhisperPath: current.ASR.WhisperPath,
		ExtraArgs:   append([]string(nil), current.ASR.ExtraArgs...),
		Shell:       current.ASR.Shell,
	})
	msg := "ASR 工具已安装并写入 config.yml"
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

	deploy, installErr := recognition.InstallOCR(ctx, mediaRoot)
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
	if installErr != nil {
		msg = installErr.Error()
	}
	if check.Message != "" {
		if installErr != nil {
			msg = msg + "；" + check.Message
		} else {
			msg = msg + "；" + check.Message
		}
	}
	ok := installErr == nil && check.OK
	c.JSON(http.StatusOK, recognitionInstallResult{
		OK:          ok,
		Message:     msg,
		Recognition: &current,
	})
}
