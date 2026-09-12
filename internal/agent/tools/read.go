package tools

import (
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
	data, err := readRootBytes(ctx, root, path, s.maxReadBytes(fromTemp))
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return imageResponse(data, mime)
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return fantasy.ToolResponse{}, errors.New("file is not UTF-8 text or a supported image")
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if in.Offset != nil {
		start = *in.Offset - 1
	}
	if start >= len(lines) {
		return fantasy.ToolResponse{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", start+1, len(lines))
	}
	end := len(lines)
	if in.Limit != nil {
		end = start + min(*in.Limit, end-start)
	}
	r := truncate(strings.Join(lines[start:end], "\n"), MaxLines, false)
	output := r.Content
	if r.FirstLineExceedsLimit {
		output = fmt.Sprintf("[Line %d exceeds the 50KB limit. Use bash to inspect a bounded byte range.]", start+1)
	} else if r.Truncated {
		output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", start+1, start+r.OutputLines, len(lines), start+r.OutputLines+1)
	} else if end < len(lines) {
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-end, end+1)
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), map[string]any{"truncation": r}), nil
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
