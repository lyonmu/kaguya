package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// BinarySniffBytes 是用于 DetectContentType / 二进制探测的文件头长度。
const BinarySniffBytes = 8 << 10

// OpenRegular 以非阻塞方式打开并确认是普通文件；失败时已关闭句柄。
// 先 open 后 stat 避免 lstat 与 open 之间的替换竞态。
func OpenRegular(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	file, err := OpenNonblock(root, name)
	if err != nil {
		return nil, nil, err
	}
	ClearNonblock(file)
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("path must be a regular file")
	}
	return file, info, nil
}

// ReadPrefix 只读取文件开头用于内容类型判定，避免为嗅探加载整个大文件。
func ReadPrefix(ctx context.Context, root *os.Root, name string, maxBytes int64) ([]byte, error) {
	file, _, err := OpenRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return buf[:n], nil
}

// ReadAll 有界读取整个普通文件；超过上限或读取中增长时返回错误。
func ReadAll(ctx context.Context, root *os.Root, name string, maxBytes int64) ([]byte, error) {
	file, info, err := OpenRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file exceeds %dMB safety limit; use bash to inspect a bounded range", maxBytes/1024/1024)
	}
	var out bytes.Buffer
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buf)
		out.Write(buf[:n])
		if int64(out.Len()) > maxBytes {
			return nil, errors.New("file grew beyond safety limit")
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return out.Bytes(), nil
}

// TextWindow 是一次有界文本读取的结果：只保留请求窗口，扫描全文件统计行数。
// 不缓存整文件，既保留“总行数、末尾截断、UTF-8 合法性”语义，又避免大文件全量驻留。
type TextWindow struct {
	Content []byte
	Lines   int  // 空文件为 1；末尾换行是行终止符，不额外产生一行。
	Binary  bool // 出现 NUL 字节或非法 UTF-8。
	Grown   bool // 扫描中文件超过安全上限。

	// OversizedLine 记录首行超过单行输出上限；调用方据此返回可操作的提示。
	OversizedLine bool
	FirstLine     bool
	OutputLines   int  // 连续装入的逻辑行数，含空行，不能从 Content 长度推断。
	Truncated     bool // 请求的行窗口被总字节/单行边界截断。
	NextLine      int  // 第一行尚未装入的行号（从 1 起）；0 表示没有后续行。
}

// ReadTextWindow 流式读取文本文件，只缓存 offset/limit 对应的行窗口。
// 以 chunk 为单位验证 UTF-8 与 NUL，跨 chunk 的不完整编码保留到下一次验证。
// lineLimit 与 outputLimit 分别限制单行和整个输出；超长行不缓存整行。
// 窗口只返回连续的完整行，截断后继续有界扫描以统计行数及验证 UTF-8。
func ReadTextWindow(ctx context.Context, root *os.Root, name string, offset, limit int, maxBytes, lineLimit, outputLimit int64) (TextWindow, error) {
	result := TextWindow{Lines: 1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if offset < 0 || limit < 1 || maxBytes < 1 || lineLimit < 0 || outputLimit < 0 {
		return result, errors.New("invalid text window limits")
	}
	file, _, err := OpenRegular(root, name)
	if err != nil {
		return result, err
	}
	defer file.Close()
	return ReadTextWindowFile(ctx, file, offset, limit, maxBytes, lineLimit, outputLimit)
}

// ReadTextWindowFile 从调用方持有的普通文件句柄读取，调用方负责关闭。
// 引用解析用该入口把读前/读后身份校验与正文读取绑定到同一实际文件。
// 每次从文件开头扫描；只缓存指定窗口，不改变现有总行数和文本验证语义。
func ReadTextWindowFile(ctx context.Context, file *os.File, offset, limit int, maxBytes, lineLimit, outputLimit int64) (TextWindow, error) {
	result := TextWindow{Lines: 1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if file == nil || offset < 0 || limit < 1 || maxBytes < 1 || lineLimit < 0 || outputLimit < 0 {
		return result, errors.New("invalid text window limits")
	}
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, errors.New("path must be a regular file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	if info.Size() > maxBytes {
		result.Grown = true
		return result, nil
	}
	var (
		window    bytes.Buffer
		pending   bytes.Buffer
		line      int
		carry     []byte
		scanned   int64
		lineBytes int64
		blocked   bool
	)
	// separator 也计入输出预算；空行占一个逻辑行，但不靠 window.Len 判断。
	lineCapacity := func() int64 {
		remaining := outputLimit - int64(window.Len())
		if result.OutputLines > 0 {
			remaining--
		}
		return min(lineLimit, max(0, remaining))
	}
	capture := func() {
		if line < offset || line-offset >= limit || blocked {
			return
		}
		separator := int64(0)
		if result.OutputLines > 0 {
			separator = 1
		}
		if lineBytes > lineLimit || int64(window.Len())+separator+lineBytes > outputLimit {
			blocked, result.Truncated = true, true
			if line == offset && lineBytes > lineLimit {
				result.OversizedLine, result.FirstLine = true, true
			}
			return
		}
		if separator != 0 {
			window.WriteByte('\n')
		}
		window.Write(pending.Bytes())
		result.OutputLines++
	}
	chunk := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		n, readErr := file.Read(chunk)
		if n > 0 {
			scanned += int64(n)
			if scanned > maxBytes {
				result.Grown = true
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
				result.Binary = true
				return result, nil
			}
			data = complete
			carry = nil
			if len(rest) > 0 {
				carry = append(carry, rest...)
			}
			if bytes.IndexByte(data, 0) >= 0 {
				result.Binary = true
				return result, nil
			}
			for len(data) > 0 {
				end := bytes.IndexByte(data, '\n')
				part := data
				if end >= 0 {
					part = data[:end]
				}
				lineBytes += int64(len(part))
				if line >= offset && line-offset < limit && !blocked {
					keep := min(int64(len(part)), max(0, lineCapacity()-int64(pending.Len())))
					pending.Write(part[:int(keep)])
				}
				if end < 0 {
					break
				}
				capture()
				line++
				lineBytes = 0
				pending.Reset()
				data = data[end+1:]
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
	if lineBytes > 0 {
		capture()
		line++
	} else if scanned == 0 {
		capture()
		line = 1
	} else if line == 0 {
		line = 1
	}
	if len(carry) > 0 {
		result.Binary = true
	}
	result.Lines = line
	result.Content = window.Bytes()
	if offset+result.OutputLines < result.Lines {
		result.NextLine = offset + result.OutputLines + 1
	}
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
