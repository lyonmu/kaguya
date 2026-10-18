package workspace

import (
	"errors"
	"io"
	"os"
	"path"
	"strconv"
)

var ErrReferenceMetadataBudget = errors.New("reference metadata budget exhausted")

// ReferenceMetadataBudget 限制扫描栈及调用方保留的候选/来源编码字节。
// 它是逻辑元信息预算，实际 Go 对象/固定读取缓冲的 heap 另行测量。
type ReferenceMetadataBudget struct {
	Limit int64
	Used  int64
}

func (b *ReferenceMetadataBudget) Take(n int64) bool {
	if n < 0 || n > b.Limit-b.Used {
		return false
	}
	b.Used += n
	return true
}

type ReferenceScan struct {
	Incomplete bool
	LastPath   string
	Reason     string
	Warnings   []string
}

// WalkReferences 顺序分批读取目录，只保留一个目录句柄和有界待扫描栈。
// visit 持有的新候选必须先从同一个 budget Take；预算耗尽返回有限 partial。
// ctx 错误保留原义，由调用方区分用户取消和局部扫描 deadline。
func (f *ReferenceFilter) WalkReferences(name string, budget *ReferenceMetadataBudget, visit func(kind, name string, info os.FileInfo) error) (ReferenceScan, error) {
	result := ReferenceScan{}
	if budget == nil || !budget.Take(int64(len(name)+32)) {
		result.Incomplete, result.Reason = true, "metadata_budget"
		return result, nil
	}
	stack := []string{name}
	// 扫描结束后待处理栈不再持有；调用方继续使用同一元信息预算。
	defer func() {
		for _, pending := range stack {
			budget.Used -= int64(len(pending) + 32)
		}
	}()
	for len(stack) > 0 {
		if err := f.ctx.Err(); err != nil {
			result.Incomplete, result.Reason = true, "deadline"
			return result, err
		}
		current := stack[len(stack)-1]
		stack[len(stack)-1] = ""
		stack = stack[:len(stack)-1]
		budget.Used -= int64(len(current) + 32)
		file, proof, err := f.OpenDirectory(current, false)
		if err != nil {
			if current == name || errors.Is(err, ErrReferenceIgnore) || f.ctx.Err() != nil {
				return result, err
			}
			warning := strconv.Quote(current) + ": directory_unavailable"
			if !budget.Take(int64(len(warning))) {
				result.Incomplete, result.Reason = true, "metadata_budget"
				return result, nil
			}
			result.Warnings = append(result.Warnings, warning)
			continue
		}
		err = f.scanDirectory(file, proof, current, budget, &stack, visit, &result)
		file.Close()
		if err != nil {
			if f.ctx.Err() != nil {
				result.Incomplete, result.Reason = true, "deadline"
			}
			return result, err
		}
		if result.Incomplete {
			return result, nil
		}
	}
	return result, nil
}

func (f *ReferenceFilter) scanDirectory(file *os.File, proof *CheckedReference, dir string, budget *ReferenceMetadataBudget, stack *[]string, visit func(kind, name string, info os.FileInfo) error, result *ReferenceScan) error {
	for {
		if err := f.ctx.Err(); err != nil {
			result.Incomplete, result.Reason = true, "deadline"
			return err
		}
		entries, readErr := file.ReadDir(128)
		if err := f.Verify(proof, file); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := f.ctx.Err(); err != nil {
				result.Incomplete, result.Reason = true, "deadline"
				return err
			}
			name := path.Join(dir, entry.Name())
			kind := "file"
			if entry.IsDir() {
				kind = "directory"
			}
			checked, err := f.Check(name, kind, true)
			if errors.Is(err, ErrReferenceFiltered) || errors.Is(err, ErrReferenceType) {
				continue
			}
			if err != nil {
				if errors.Is(err, ErrReferenceIgnore) || f.ctx.Err() != nil {
					return err
				}
				// 子项失效不伪报已扫描完整；错误不含 OS 路径/内容。
				warning := strconv.Quote(name) + ": entry_unavailable"
				if budget.Take(int64(len(warning))) {
					result.Warnings = append(result.Warnings, warning)
				} else {
					result.Incomplete, result.Reason = true, "metadata_budget"
					return nil
				}
				continue
			}
			if kind == "directory" {
				if !budget.Take(int64(len(name) + 32)) {
					result.Incomplete, result.Reason = true, "metadata_budget"
					return nil
				}
				*stack = append(*stack, name)
			}
			if err := visit(kind, name, checked.Info); err != nil {
				if errors.Is(err, ErrReferenceMetadataBudget) {
					result.Incomplete, result.Reason = true, "metadata_budget"
					return nil
				}
				return err
			}
			result.LastPath = name
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return ErrReferenceUnavailable
		}
	}
}
