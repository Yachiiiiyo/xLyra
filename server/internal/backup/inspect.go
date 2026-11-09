package backup

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// inspectManifestMaxBytes 限制解压后的 manifest.json 大小,防止异常备份
// 在预览阶段占用过多内存。
const inspectManifestMaxBytes = 1 << 20

// InspectUnavailable 描述备份里包含、但当前实例无法恢复的类别(例如
// 没有配置 Agent 数据目录时的 agent 文件类别)。
type InspectUnavailable struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// InspectResult 是恢复前预览的结果:备份包含哪些类别、恢复会清空哪些
// 现有数据、哪些类别当前无法恢复。
type InspectResult struct {
	FormatVersion int                  `json:"format_version"`
	CreatedAt     time.Time            `json:"created_at"`
	Legacy        bool                 `json:"legacy"`   // v2/v3 旧备份(没有 sections 清单)
	Sections      []string             `json:"sections"` // effectiveSections:本次会恢复的类别
	Impact        []SectionImpact      `json:"impact"`   // 会被清空的其它类别
	Unavailable   []InspectUnavailable `json:"unavailable"`
}

// InspectReader 在不恢复的前提下预览加密备份:只解密第一个分块并从 zip
// 开头解出 manifest,不读取后面的内容,因此传入备份文件的开头一段即可。
func (s Service) InspectReader(ctx context.Context, passphrase string, encrypted io.Reader) (InspectResult, error) {
	if strings.TrimSpace(passphrase) == "" {
		return InspectResult{}, fmt.Errorf("backup passphrase is required")
	}
	if encrypted == nil {
		return InspectResult{}, fmt.Errorf("backup file is required")
	}
	chunk, err := decryptFirstChunk(passphrase, &backupContextReader{ctx: ctx, reader: encrypted})
	if err != nil {
		return InspectResult{}, err
	}
	var m manifest
	if err := decodeInspectManifest(chunk, &m); err != nil {
		return InspectResult{}, err
	}
	if err := validateManifest(m); err != nil {
		return InspectResult{}, err
	}
	sections := effectiveSections(m)
	impact := RestoreImpact(sections)
	if impact == nil {
		impact = []SectionImpact{}
	}
	unavailable := []InspectUnavailable{}
	for _, id := range sections {
		if available, reason := s.sectionAvailability(id); !available {
			unavailable = append(unavailable, InspectUnavailable{ID: id, Reason: reason})
		}
	}
	return InspectResult{
		FormatVersion: m.FormatVersion,
		CreatedAt:     m.CreatedAt,
		Legacy:        m.FormatVersion < 4,
		Sections:      sections,
		Impact:        impact,
		Unavailable:   unavailable,
	}, nil
}

// decodeInspectManifest 从第一个块的明文开头手工解析 zip 本地文件头并解出
// manifest.json。writeArchive 流式写入 manifest(Deflate + data descriptor),
// 但 Deflate 流自带结束标记,不需要 data descriptor 里的压缩后大小。
func decodeInspectManifest(chunk []byte, dst *manifest) error {
	missing := fmt.Errorf("backup archive missing manifest.json")
	// 30 = zip 本地文件头固定部分的长度(签名到 extra 长度字段)。
	const localHeaderSize = 30
	if len(chunk) < localHeaderSize {
		return missing
	}
	if !bytes.Equal(chunk[:4], []byte("PK\x03\x04")) {
		return missing
	}
	method := binary.LittleEndian.Uint16(chunk[8:10])
	nameLen := int(binary.LittleEndian.Uint16(chunk[26:28]))
	extraLen := int(binary.LittleEndian.Uint16(chunk[28:30]))
	if localHeaderSize+nameLen+extraLen > len(chunk) {
		return missing
	}
	if string(chunk[localHeaderSize:localHeaderSize+nameLen]) != "manifest.json" {
		return missing
	}
	data := chunk[localHeaderSize+nameLen+extraLen:]
	switch method {
	case zip.Store:
	case zip.Deflate:
		reader := flate.NewReader(bytes.NewReader(data))
		defer reader.Close()
		decompressed, err := io.ReadAll(io.LimitReader(reader, inspectManifestMaxBytes+1))
		if err != nil {
			return fmt.Errorf("decode manifest.json: %w", err)
		}
		if len(decompressed) > inspectManifestMaxBytes {
			return fmt.Errorf("manifest.json exceeds %d bytes", inspectManifestMaxBytes)
		}
		data = decompressed
	default:
		return fmt.Errorf("unsupported manifest compression method %d", method)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode manifest.json: %w", err)
	}
	return nil
}
