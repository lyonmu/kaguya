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
	"strings"
	"unicode/utf8"

	"charm.land/fantasy"
)

type ReadInput struct {
	Path   string `json:"path" description:"Path to the file (relative or absolute inside the workspace)."`
	Offset *int   `json:"offset,omitempty" description:"1-indexed starting line. Default 1."`
	Limit  *int   `json:"limit,omitempty" description:"Maximum number of lines to read."`
}

func (s *Set) ReadTool() fantasy.AgentTool {
	return tool(s, "read", "Read text or images (jpg, png, gif, webp, bmp). Text is limited to 2000 lines or 50KB; use offset/limit to continue. Images are returned as attachments. Paths must be inside the project workspace.", s.read)
}
func (s *Set) readBytes(ctx context.Context, path string) ([]byte, error) {
	f, err := openReadFile(s.root, path)
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
	if info.Size() > MaxFileBytes {
		return nil, fmt.Errorf("file exceeds %dMB safety limit; use bash to inspect a bounded range", MaxFileBytes/1024/1024)
	}
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := f.Read(buf)
		out.Write(buf[:n])
		if out.Len() > MaxFileBytes {
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
func (s *Set) read(ctx context.Context, in ReadInput) (fantasy.ToolResponse, error) {
	if in.Offset != nil && *in.Offset < 1 || in.Limit != nil && *in.Limit < 1 {
		return fantasy.ToolResponse{}, errors.New("offset and limit must be positive integers")
	}
	path, err := s.resolve(in.Path)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	data, err := s.readBytes(ctx, path)
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
