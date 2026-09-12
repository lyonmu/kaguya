package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/fantasy"
)

type ReadInput struct {
	Path   string `json:"path" description:"One existing file, not a directory or glob. Prefer a project-relative path, e.g. src/main.go."`
	Offset *int   `json:"offset,omitempty" description:"First text line to return, counting from 1 (not a byte offset). Omit to start at 1."`
	Limit  *int   `json:"limit,omitempty" description:"Positive number of text lines to return, not an ending line number. Omit for up to 2000 lines."`
}

// binarySniffBytes 是用于 DetectContentType 的文件头长度。
const binarySniffBytes = 8 << 10

func (s *Set) ReadTool() fantasy.AgentTool {
	return tool(s, "read", `Read one existing text file or image inside the project workspace, or a temporary bash output path returned during this conversation. Other paths outside the workspace are rejected. To list/search paths, use bash instead. Text returns at most 2000 lines or 50KB; follow the returned next offset when truncated. offset and limit are 1-based start line and line count, not a range string. Images (jpg/png/gif/webp/bmp) are returned as attachments; omit offset/limit for images. Example: {"path":"src/main.go","offset":20,"limit":80}.`, s.read)
}

func readRootBytes(ctx context.Context, root *os.Root, path string, maxBytes int64) ([]byte, error) {
	f, err := openReadFile(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file")
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %dMB safety limit; use bash to inspect a bounded range", maxBytes/1024/1024)
	}
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buf)
		out.Write(buf[:n])
		if int64(out.Len()) > maxBytes {
			return nil, errors.New("file grew beyond safety limit")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}

// readWindow 是一次有界文本读取的结果：只保留请求窗口，扫描全文件统计行数。
// 不缓存整文件，既保留“总行数、末尾截断、UTF-8 合法性”语义，又避免大文件全量驻留。
type readWindow struct {
	content []byte
	lines   int  // strings.Split(content, "\n") 的行数语义：空文件为 1，末尾换行后是空行。
	binary  bool // 出现 NUL 字节或非法 UTF-8。
	grown   bool // 扫描中文件超过安全上限。

	// oversizedLine 记录首行超过单行输出上限；调用方据此返回可操作的提示。
	oversizedLine bool
	firstLine     bool
}

// readTextWindow 流式读取文本文件，只缓存 offset/limit 对应的行窗口。
// 以 chunk 为单位验证 UTF-8 与 NUL，跨 chunk 的不完整编码保留到下一次验证。
func readTextWindow(ctx context.Context, root *os.Root, path string, offset, limit int, maxBytes int64) (readWindow, error) {
	result := readWindow{lines: 1}
	f, err := openReadFile(root, path)
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("path must be a regular file")
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	var (
		window  bytes.Buffer
		pending bytes.Buffer
		line    int
		carry   []byte
		scanned int64
	)
	chunk := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, readErr := reader.Read(chunk)
		if n > 0 {
			scanned += int64(n)
			if scanned > maxBytes {
				result.grown = true
				break
			}
			// 拼接上一次遗留的不完整序列后统一扫描；carry 最多 3 字节。
			// 没有遗留时直接扫描读取缓冲，避免每个 chunk 复制一次。
			data := chunk[:n]
			if len(carry) > 0 {
				data = append(carry, chunk[:n]...)
			}
			complete, rest, invalid := scanUTF8(data)
			if invalid {
				result.binary = true
			}
			data = complete
			carry = nil
			if len(rest) > 0 {
				carry = append(carry, rest...)
			}
			if bytes.IndexByte(data, 0) >= 0 {
				result.binary = true
			}
			for _, b := range data {
				if b != '\n' {
					pending.WriteByte(b)
					continue
				}
				if oversized := captureLine(&window, pending.Bytes(), line, offset, limit, maxBytes); oversized && line == offset {
					result.oversizedLine, result.firstLine = true, true
				}
				line++
				pending.Reset()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return result, readErr
		}
	}
	// 扫描结束时处理最后一行：没有末尾换行的内容也算一行；空文件记为 1 行。
	if pending.Len() > 0 {
		if oversized := captureLine(&window, pending.Bytes(), line, offset, limit, maxBytes); oversized && line == offset {
			result.oversizedLine, result.firstLine = true, true
		}
		line++
	} else if scanned == 0 {
		line = 1
	} else if line == 0 {
		line = 1
	}
	if len(carry) > 0 {
		result.binary = true
	}
	result.lines = line
	result.content = window.Bytes()
	return result, nil
}

// scanUTF8 返回本次可安全消费的前缀与需要留给下一次验证的末尾不完整序列。
// invalid 表示出现无论后续字节如何都不可能合法的序列（例如孤立的续字节）。
func scanUTF8(data []byte) (complete, carry []byte, invalid bool) {
	start := 0
	for start < len(data) {
		if data[start] < utf8.RuneSelf {
			start++
			continue
		}
		valid, size := runePrefix(data[start:])
		switch {
		case size > 0:
			start += size
		case valid:
			return data[:start], data[start:], false
		default:
			return data[:start], nil, true
		}
	}
	return data, nil, false
}

// runePrefix 校验 data 开头的 rune：size 大于 0 表示已完整消费的字节数；
// 否则 valid 表示只是字节不足，等待下一次读取继续验证。
func runePrefix(data []byte) (valid bool, size int) {
	if len(data) == 0 {
		return false, 0
	}
	b := data[0]
	var need int
	switch {
	case b >= 0xC2 && b <= 0xDF:
		need = 2
	case b >= 0xE0 && b <= 0xEF:
		need = 3
	case b >= 0xF0 && b <= 0xF4:
		need = 4
	default:
		return false, 0
	}
	if len(data) < need {
		// 前导字节合法但后续字节不足：检查已读部分是否是合法前缀。
		for i := 1; i < len(data); i++ {
			if data[i]&0xC0 != 0x80 {
				return false, 0
			}
		}
		return true, 0
	}
	if !utf8.Valid(data[:need]) {
		return false, 0
	}
	return true, need
}

// captureLine 按行号把行内容写入窗口；超长行只标记不返回，避免单行占满内存。
// 返回 true 表示该行超过单行输出上限（MaxBytes）而被跳过。
func captureLine(window *bytes.Buffer, content []byte, line, offset, limit int, _ int64) bool {
	if line < offset || line >= offset+limit {
		return false
	}
	if len(content) > MaxBytes {
		return true
	}
	if window.Len() > 0 {
		window.WriteByte('\n')
	}
	window.Write(content)
	return false
}

func (s *Set) readBytes(ctx context.Context, path string) ([]byte, error) {
	return readRootBytes(ctx, s.root, path, MaxFileBytes)
}

// resolveRead 接受项目内路径，或本会话的 bash 完整输出路径（含旧日期）。
func (s *Set) resolveRead(path string) (*os.Root, string, bool, error) {
	if rel, ok := s.bashOutputRelative(path); ok {
		root, err := os.OpenRoot(s.tempBase)
		if err != nil {
			return nil, "", false, err
		}
		return root, rel, true, nil
	}
	path, err := s.resolve(path)
	if err != nil {
		return nil, "", false, err
	}
	return s.root, path, false, nil
}

// maxReadBytes 按来源选择读取上限：项目文件受限，本会话输出可到单命令配额。
func (s *Set) maxReadBytes(fromTemp bool) int64 {
	if fromTemp {
		return maxCommandOutputBytes
	}
	return MaxFileBytes
}

// bashOutputRelative 把绝对路径识别为本次会话的输出文件，返回 tempBase 相对路径。
// 只接受 <app>/<date>/<conversation>/bash-<id>.log 结构，拒绝其它会话与任意文件。
func (s *Set) bashOutputRelative(path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(s.tempBase, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != toolOutputAppDir || parts[2] != s.conversationID || !validBashOutputName(parts[3]) {
		return "", false
	}
	return filepath.FromSlash(strings.Join(parts, "/")), true
}

func validBashOutputName(name string) bool {
	idText, ok := strings.CutPrefix(name, "bash-")
	if !ok {
		return false
	}
	idText, ok = strings.CutSuffix(idText, ".log")
	if !ok {
		return false
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == idText
}

func (s *Set) read(ctx context.Context, in ReadInput) (fantasy.ToolResponse, error) {
	if in.Offset != nil && *in.Offset < 1 || in.Limit != nil && *in.Limit < 1 {
		return fantasy.ToolResponse{}, errors.New("offset and limit must be positive integers")
	}
	root, path, fromTemp, err := s.resolveRead(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if fromTemp {
		defer root.Close()
	}
	maxBytes := s.maxReadBytes(fromTemp)

	// 先读文件头判定图片；文本路径不再全量加载，只流式扫描并缓存请求窗口。
	header, err := readRootPrefix(ctx, root, path, binarySniffBytes)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	mime := http.DetectContentType(header)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		data, err := readRootBytes(ctx, root, path, maxBytes)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		return imageResponse(data, mime)
	}

	start := 0
	if in.Offset != nil {
		start = *in.Offset - 1
	}
	limit := MaxLines
	if in.Limit != nil {
		limit = *in.Limit
	}
	window, err := readTextWindow(ctx, root, path, start, limit, maxBytes)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if window.grown {
		return fantasy.ToolResponse{}, errors.New("file grew beyond safety limit")
	}
	if window.binary {
		return fantasy.ToolResponse{}, errors.New("file is not UTF-8 text or a supported image")
	}
	if start >= window.lines {
		return fantasy.ToolResponse{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", start+1, window.lines)
	}
	if window.oversizedLine {
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(fmt.Sprintf("[Line %d exceeds the 50KB limit. Use bash to inspect a bounded byte range.]", start+1)), map[string]any{"truncation": Truncation{FirstLineExceedsLimit: true}}), nil
	}
	r := truncate(string(window.content), MaxLines, false)
	// 空文件等场景窗口内容为空但确实存在一行，保持与旧行为一致不提示续读。
	if r.OutputLines == 0 && len(window.content) == 0 {
		r.OutputLines = 1
	}
	output := r.Content
	end := start + r.OutputLines
	if end > window.lines {
		end = window.lines
	}
	if r.Truncated {
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", start+1, end, window.lines, end+1)
	} else if end < window.lines {
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", window.lines-end, end+1)
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), map[string]any{"truncation": r}), nil
}

// readRootPrefix 只读取文件开头用于内容类型判定，避免为了嗅探而加载整个大文件。
func readRootPrefix(ctx context.Context, root *os.Root, path string, maxBytes int64) ([]byte, error) {
	f, err := openReadFile(root, path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}
func imageResponse(data []byte, mime string) (fantasy.ToolResponse, error) {
	// PNG/JPEG/GIF can be decoded by the standard library. Preserve other supported
	// formats as attachments; never pretend a text dump is a successful image read.
	if len(data) > 10*1024*1024 {
		return fantasy.ToolResponse{}, errors.New("image exceeds 10MB attachment limit")
	}
	if mime != "image/webp" && mime != "image/bmp" {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return fantasy.ToolResponse{}, errors.New("image dimensions exceed safety limit")
		}
		if cfg.Width > 2000 || cfg.Height > 2000 {
			src, _, err := image.Decode(bytes.NewReader(data))
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			w, h := cfg.Width, cfg.Height
			if w >= h {
				h = max(1, h*2000/w)
				w = 2000
			} else {
				w = max(1, w*2000/h)
				h = 2000
			}
			dst := image.NewNRGBA(image.Rect(0, 0, w, h))
			bounds := src.Bounds()
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					dst.Set(x, y, src.At(bounds.Min.X+x*cfg.Width/w, bounds.Min.Y+y*cfg.Height/h))
				}
			}
			var buf bytes.Buffer
			if err := png.Encode(&buf, dst); err != nil {
				return fantasy.ToolResponse{}, err
			}
			data = buf.Bytes()
			mime = "image/png"
		}
	}
	if len(data) > 10*1024*1024 {
		return fantasy.ToolResponse{}, errors.New("processed image exceeds attachment limit")
	}
	return fantasy.NewImageResponse(data, mime), nil
}

// ReadReference reuses the file tool's workspace checks and output limits.
func (s *Set) ReadReference(ctx context.Context, path string) (string, error) {
	response, err := s.read(ctx, ReadInput{Path: path})
	if err != nil {
		return "", err
	}
	if response.Type != "text" {
		return "", errors.New("file references currently require UTF-8 text")
	}
	return response.Content, nil
}
