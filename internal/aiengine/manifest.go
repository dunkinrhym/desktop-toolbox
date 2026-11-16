// Package aiengine 管理离线 AI 组件（下载 / 校验 / 删除）并与 Python AI 引擎（ai-engine/dtai）通信。
//
// 所有组件首次使用时下载到 <数据目录>/ai，下载完成后完全离线运行：
//
//	<root>/site/<id>/    Python 轮子（解压后的包，按组件分目录）
//	<root>/models/<id>/  模型文件
//	<root>/bin/<id>/     可执行程序（llama.cpp）
//
// 组件清单 manifest.json 由 ai-engine/tools/gen_manifest.py 生成（主站 + 镜像地址、大小、SHA256）。
package aiengine

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
)

//go:embed manifest.json
var manifestJSON []byte

// File 组件中的一个下载文件。
type File struct {
	Name    string   `json:"name"`
	Size    int64    `json:"size"`
	SHA256  string   `json:"sha256"`
	URLs    []string `json:"urls"`
	Extract string   `json:"extract"` // zip | tgz | file
}

// Component 一个可单独下载 / 删除的组件。
type Component struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"` // site | models | bin
	Name     string            `json:"name"`
	Desc     string            `json:"desc"`
	Version  string            `json:"version"`
	MinRAMGB int               `json:"minRamGB,omitempty"`
	Hidden   bool              `json:"hidden,omitempty"`
	Files    map[string][]File `json:"files"`
}

// Manifest 组件清单。
type Manifest struct {
	Version    int                 `json:"version"`
	Components []Component         `json:"components"`
	Features   map[string][]string `json:"features"`
}

var (
	manifestOnce sync.Once
	manifest     *Manifest
	manifestErr  error
)

// LoadManifest 返回内嵌的组件清单。
func LoadManifest() (*Manifest, error) {
	manifestOnce.Do(func() {
		var m Manifest
		manifestErr = json.Unmarshal(manifestJSON, &m)
		manifest = &m
	})
	return manifest, manifestErr
}

// Platform 当前平台键（GOOS-GOARCH），与 manifest 中的键一致。
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// FilesFor 返回组件在当前平台需要下载的文件；不支持当前平台时返回 nil。
func (c *Component) FilesFor(platform string) []File {
	if fs, ok := c.Files["all"]; ok {
		return fs
	}
	return c.Files[platform]
}

// TotalSize 当前平台的下载大小。
func (c *Component) TotalSize(platform string) int64 {
	var n int64
	for _, f := range c.FilesFor(platform) {
		n += f.Size
	}
	return n
}

// Component 按 ID 查找组件。
func (m *Manifest) Component(id string) (*Component, error) {
	for i := range m.Components {
		if m.Components[i].ID == id {
			return &m.Components[i], nil
		}
	}
	return nil, fmt.Errorf("未知组件：%s", id)
}

// LLMIDs 大模型组件（从大到小）。
var LLMIDs = []string{"llm-8b", "llm-4b", "llm-1.7b", "llm-0.6b"}

// IsLLM 是否为大模型组件。
func IsLLM(id string) bool {
	for _, x := range LLMIDs {
		if x == id {
			return true
		}
	}
	return false
}

// RecommendLLM 按物理内存推荐模型：≥15GB（16GB 机器通常报告 15.x GB）用 8B，≥7GB 用 4B，否则 1.7B。
func RecommendLLM(totalRAM uint64) string {
	gb := float64(totalRAM) / (1 << 30)
	switch {
	case gb >= 15:
		return "llm-8b"
	case gb >= 7:
		return "llm-4b"
	default:
		return "llm-1.7b"
	}
}

// FeatureComponents 某功能需要的全部组件。llm 为使用的大模型组件 ID（功能需要大模型时）。
func (m *Manifest) FeatureComponents(feature, llm string) []string {
	switch feature {
	case "translate", "rewrite", "summary":
		return []string{"llama", llm}
	case "meeting":
		return append([]string{}, m.Features["asr"]...)
	}
	return append([]string{}, m.Features[feature]...)
}
