package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SaveSubtitleRecognition updates subtitle.auto_on_scan, subtitle.asr, subtitle.graphical_ocr
// and subtitle.ai_proofread in config.yml, preserving other keys and comments where possible.
func SaveSubtitleRecognition(path string, autoOnScan bool, asr ASRConfig, ocr GraphicalOCRConfig, aiProofread bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRoot(&doc)
	if err != nil {
		return err
	}
	subtitle := ensureMapKey(root, "subtitle")
	if err := setScalarKey(subtitle, "auto_on_scan", autoOnScan); err != nil {
		return fmt.Errorf("patch auto_on_scan: %w", err)
	}
	if err := setStructKey(subtitle, "asr", asr); err != nil {
		return fmt.Errorf("patch asr: %w", err)
	}
	if err := setStructKey(subtitle, "graphical_ocr", ocr); err != nil {
		return fmt.Errorf("patch graphical_ocr: %w", err)
	}
	if err := setScalarKey(subtitle, "ai_proofread", aiProofread); err != nil {
		return fmt.Errorf("patch ai_proofread: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SavePhotoClassify updates photo_classify in config.yml, preserving other keys.
func SavePhotoClassify(path string, pc PhotoClassifyConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRoot(&doc)
	if err != nil {
		return err
	}
	if err := setStructKey(root, "photo_classify", pc); err != nil {
		return fmt.Errorf("patch photo_classify: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SavePhotoFace updates photo_face in config.yml, preserving other keys.
func SavePhotoFace(path string, pf PhotoFaceConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRoot(&doc)
	if err != nil {
		return err
	}
	if err := setStructKey(root, "photo_face", pf); err != nil {
		return fmt.Errorf("patch photo_face: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SaveDocTrans updates doc_trans in config.yml, preserving other keys.
func SaveDocTrans(path string, dt DocTransConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRoot(&doc)
	if err != nil {
		return err
	}
	if err := setStructKey(root, "doc_trans", dt); err != nil {
		return fmt.Errorf("patch doc_trans: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// SaveBranding updates branding in config.yml, preserving other keys.
func SaveBranding(path string, b BrandingConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	root, err := documentRoot(&doc)
	if err != nil {
		return err
	}
	if err := setStructKey(root, "branding", b); err != nil {
		return fmt.Errorf("patch branding: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func documentRoot(doc *yaml.Node) (*yaml.Node, error) {
	if doc == nil || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty yaml document")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping root")
	}
	return root, nil
}

func ensureMapKey(parent *yaml.Node, key string) *yaml.Node {
	idx := mapKeyIndex(parent, key)
	if idx >= 0 {
		val := parent.Content[idx+1]
		if val.Kind != yaml.MappingNode {
			val.Kind = yaml.MappingNode
			val.Tag = "!!map"
			val.Content = nil
		}
		return val
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode, valNode)
	return valNode
}

func mapKeyIndex(parent *yaml.Node, key string) int {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func setStructKey(parent *yaml.Node, key string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	var wrapper yaml.Node
	if err := yaml.Unmarshal(raw, &wrapper); err != nil {
		return err
	}
	mapping, err := documentRoot(&wrapper)
	if err != nil {
		return err
	}
	idx := mapKeyIndex(parent, key)
	if idx >= 0 {
		parent.Content[idx+1] = mapping
		return nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	parent.Content = append(parent.Content, keyNode, mapping)
	return nil
}

func setScalarKey(parent *yaml.Node, key string, v bool) error {
	idx := mapKeyIndex(parent, key)
	valNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%t", v)}
	if idx >= 0 {
		parent.Content[idx+1] = valNode
		return nil
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	parent.Content = append(parent.Content, keyNode, valNode)
	return nil
}
