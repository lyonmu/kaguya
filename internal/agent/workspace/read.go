package workspace

import (
	"bufio"
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
	Lines   int  // strings.Split(content, "\n") 的行数语义：空文件为 1，末尾换行后是空行。
	Binary  bool // 出现 NUL 字节或非法 UTF-8。
	Grown   bool // 扫描中文件超过安全上限。

	// OversizedLine 记录首行超过单行输出上限；调用方据此返回可操作的提示。
	OversizedLine bool
	FirstLine     bool
}

// ReadTextWindow 流式读取文本文件，只缓存 offset/limit 对应的行窗口。
// 以 chunk 为单位验证 UTF-8 与 NUL，跨 chunk 的不完整编码保留到下一次验证。
// lineLimit 限制单行进入窗口的字节数，超过则不返回内容只标记 OversizedLine。
func ReadTextWindow(ctx context.Context, root *os.Root, name string, offset, limit int, maxBytes, lineLimit int64) (TextWindow, error) {
	result := TextWindow{Lines: 1}
	file, info, err := OpenRegular(root, name)
	if err != nil {
		return result, err
	}
	defer file.Close()
	if info.Size() > maxBytes {
		result.Grown = true
		return result, nil
	}
	reader := bufio.NewReaderSize(file, 64*1024)
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
			}
			data = complete
			carry = nil
			if len(rest) > 0 {
				carry = append(carry, rest...)
			}
			if bytes.IndexByte(data, 0) >= 0 {
				result.Binary = true
			}
			for _, b := range data {
				if b != '\n' {
					pending.WriteByte(b)
					continue
				}
				if oversized := captureLine(&window, pending.Bytes(), line, offset, limit, lineLimit); oversized && line == offset {
					result.OversizedLine, result.FirstLine = true, true
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
		if oversized := captureLine(&window, pending.Bytes(), line, offset, limit, lineLimit); oversized && line == offset {
			result.OversizedLine, result.FirstLine = true, true
		}
		line++
	} else if scanned == 0 {
		line = 1
	} else if line == 0 {
		line = 1
	}
	if len(carry) > 0 {
		result.Binary = true
	}
	result.Lines = line
	result.Content = window.Bytes()
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
// 返回 true 表示该行超过单行输出上限而被跳过。
func captureLine(window *bytes.Buffer, content []byte, line, offset, limit int, lineLimit int64) bool {
	if line < offset || line >= offset+limit {
		return false
	}
	if int64(len(content)) > lineLimit {
		return true
	}
	if window.Len() > 0 {
		window.WriteByte('\n')
	}
	window.Write(content)
	return false
}
