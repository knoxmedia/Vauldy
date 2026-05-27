// Command doctrans-test runs document conversion engine detection and sample conversions on Windows.
package main

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"knox-media/internal/config"
	"knox-media/internal/doctrans"
)

func main() {
	cfgPath := flag.String("config", "config.yml", "config file path")
	mediaRoot := flag.String("root", ".", "media project root")
	installLO := flag.Bool("install-lo", false, "attempt LibreOffice one-click install before testing")
	engine := flag.String("engine", "", "force single engine: office|wps|libreoffice (default: use config order)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	root := filepath.Clean(*mediaRoot)
	ctx := context.Background()

	fmt.Println("=== Knox Media — 文档转换联调 ===")
	fmt.Printf("项目根: %s\n\n", root)

	if *installLO {
		fmt.Println(">> 尝试安装 LibreOffice …")
		deploy, err := doctrans.InstallLibreOffice(ctx, root)
		if err != nil {
			fmt.Printf("   安装失败: %v\n", err)
		} else {
			fmt.Printf("   检测到: %s\n", deploy.LibreOfficePath)
			cfg.DocTrans.LibreOfficePath = deploy.LibreOfficePath
			cfg.DocTrans.SofficePath = deploy.SofficePath
		}
		fmt.Println()
	}

	check := doctrans.CheckConfig(ctx, root, cfg.DocTrans)
	printEngines(check.Engines, check.ActiveEngine, check.Message)
	if !check.OK {
		fmt.Println("\n没有可用引擎。可运行: doctrans-test -install-lo")
		os.Exit(1)
	}

	fixDir := filepath.Join(root, "tools", "doctran", "fixtures")
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		fatalf("fixtures dir: %v", err)
	}
	samples := ensureFixtures(fixDir)
	fmt.Printf("\n测试样例目录: %s\n", fixDir)

	order := cfg.DocTrans.EngineOrder
	if *engine != "" {
		order = []string{strings.ToLower(strings.TrimSpace(*engine))}
	}
	previewRoot := cfg.Data.Preview

	for _, kind := range order {
		k := doctrans.EngineKind(kind)
		st := engineStatus(root, cfg.DocTrans, k)
		fmt.Printf("\n--- 引擎: %s ---\n", st.Label)
		if !st.Available {
			fmt.Printf("跳过: %s\n", st.Message)
			continue
		}
		if st.Path != "" {
			fmt.Printf("路径: %s\n", st.Path)
		}
		for _, sample := range samples {
			runOne(ctx, root, previewRoot, cfg.DocTrans, k, sample)
		}
	}
	fmt.Println("\n联调完成。")
}

func printEngines(engines []doctrans.EngineStatus, active, summary string) {
	fmt.Println("引擎检测:")
	for _, e := range engines {
		status := "不可用"
		if e.Available {
			status = "可用"
		}
		line := fmt.Sprintf("  [%s] %s — %s", status, e.Label, e.Message)
		if e.Path != "" {
			line += fmt.Sprintf(" (%s)", e.Path)
		}
		if e.Version != "" && len(e.Version) < 120 {
			line += fmt.Sprintf(" %s", e.Version)
		}
		fmt.Println(line)
	}
	if summary != "" {
		fmt.Printf("\n结论: %s", summary)
		if active != "" {
			fmt.Printf(" [%s]", active)
		}
		fmt.Println()
	}
}

func engineStatus(root string, cfg config.DocTransConfig, kind doctrans.EngineKind) doctrans.EngineStatus {
	for _, st := range doctrans.DetectEngines(root, cfg) {
		if st.Kind == kind {
			return st
		}
	}
	return doctrans.EngineStatus{Kind: kind, Message: "unknown"}
}

func runOne(ctx context.Context, root, previewRoot string, cfg config.DocTransConfig, kind doctrans.EngineKind, sample string) {
	base := filepath.Base(sample)
	fmt.Printf("  转换 %s … ", base)
	tmpDir, err := os.MkdirTemp("", "doctrans-test-*")
	if err != nil {
		fmt.Printf("FAIL (tmpdir): %v\n", err)
		return
	}
	defer os.RemoveAll(tmpDir)

	cctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	if cfg.TimeoutSeconds <= 0 {
		cancel()
		cctx, cancel = context.WithTimeout(ctx, 180*time.Second)
	}
	defer cancel()

	single := cfg
	single.EngineOrder = []string{string(kind)}
	conv := doctrans.NewConverter(root, previewRoot, single)
	pdf, err := conv.EnsurePreviewPDF(cctx, 99999, sample, fileMtime(sample))
	if err != nil {
		fmt.Printf("FAIL: %v\n", err)
		return
	}
	st, err := os.Stat(pdf)
	if err != nil || st.Size() == 0 {
		fmt.Printf("FAIL: empty output\n")
		return
	}
	fmt.Printf("OK (%d KB) -> %s\n", st.Size()/1024, pdf)
}

func fileMtime(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().Unix()
}

func ensureFixtures(dir string) []string {
	docx := filepath.Join(dir, "sample.docx")
	if err := writeMinimalDocx(docx); err != nil {
		fatalf("create docx: %v", err)
	}
	return []string{docx}
}

func writeMinimalDocx(path string) error {
	if st, err := os.Stat(path); err == nil && st.Size() > 0 {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	files := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Knox Media 文档转换联调测试</w:t></w:r></w:p>
    <w:p><w:r><w:t>Conversion integration test.</w:t></w:r></w:p>
  </w:body>
</w:document>`,
	}
	for name, body := range files {
		h, err := w.Create(name)
		if err != nil {
			return err
		}
		if _, err := h.Write([]byte(body)); err != nil {
			return err
		}
	}
	return w.Close()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
