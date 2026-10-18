package workspace

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrReferenceFiltered    = errors.New("reference is filtered")
	ErrReferenceType        = errors.New("reference type is invalid")
	ErrReferenceChanged     = errors.New("reference changed during access")
	ErrReferenceUnavailable = errors.New("reference is unavailable")
	ErrReferenceIgnore      = errors.New("reference ignore rules are unavailable")
)

const referencePathLimit = 4096

// ReferenceFilter 是候选和引用正文共享的严格策略，不改变工具/预览的访问策略。
type ReferenceFilter struct {
	ctx     context.Context
	root    *os.Root
	matcher *IgnoreMatcher
}

func NewReferenceFilter(ctx context.Context, root *os.Root, matcher *IgnoreMatcher) *ReferenceFilter {
	return &ReferenceFilter{ctx: ctx, root: root, matcher: matcher}
}

type referenceBinding struct {
	path string
	info os.FileInfo
}

// CheckedReference 只是一份短期访问证明；实际打开和读取后还要 Verify。
// Path 是已校验的 root-relative 目标；原始 alias 由调用方保存作来源关系。
type CheckedReference struct {
	Path     string
	Info     os.FileInfo
	bindings []referenceBinding
}

// Check 检查词法祖先、ignore、实际类型及所有 symlink 目标。
// automatic 跳过隐藏/临时项；显式文件允许未被过滤的隐藏文本。
// 目录从不跟随 symlink（包括路径中的目录 alias）。
func (f *ReferenceFilter) Check(name, kind string, automatic bool) (*CheckedReference, error) {
	if err := f.ctx.Err(); err != nil {
		return nil, err
	}
	if f.root == nil || f.matcher == nil || !f.matcher.strict {
		return nil, ErrReferenceIgnore
	}
	if kind != "file" && kind != "directory" {
		return nil, ErrReferenceType
	}
	clean, err := CleanRelative(name, referencePathLimit)
	if err != nil {
		return nil, ErrReferenceFiltered
	}
	name = path.Clean(filepath.ToSlash(clean))
	proof := &CheckedReference{}
	links, proofBytes := 0, 0
	for {
		if err := f.checkPolicy(name, kind, automatic); err != nil {
			return nil, err
		}
		parts := strings.Split(name, "/")
		prefix, expanded := "", false
		for i, part := range parts {
			if err := f.ctx.Err(); err != nil {
				return nil, err
			}
			prefix = path.Join(prefix, part)
			info, err := f.root.Lstat(prefix)
			if err != nil {
				return nil, ErrReferenceUnavailable
			}
			// 证明本身也有界，超深/重复链接不积累无界路径对象。
			proofBytes += len(prefix) + 64
			if proofBytes > 64<<10 {
				return nil, ErrReferenceFiltered
			}
			proof.bindings = append(proof.bindings, referenceBinding{prefix, info})
			if info.Mode()&os.ModeSymlink != 0 {
				if kind == "directory" || automatic {
					return nil, ErrReferenceFiltered
				}
				links++
				if links > 40 {
					return nil, ErrReferenceFiltered
				}
				target, err := f.root.Readlink(prefix)
				if err != nil {
					return nil, ErrReferenceUnavailable
				}
				// 绝对目标无法用 root-relative 证明其 policy，保守拒绝。
				if filepath.IsAbs(target) {
					return nil, ErrReferenceFiltered
				}
				joined := filepath.Join(filepath.FromSlash(path.Dir(prefix)), target)
				if i+1 < len(parts) {
					joined = filepath.Join(joined, filepath.FromSlash(strings.Join(parts[i+1:], "/")))
				}
				clean, err := CleanRelative(joined, referencePathLimit)
				if err != nil {
					return nil, ErrReferenceFiltered
				}
				name = path.Clean(filepath.ToSlash(clean))
				expanded = true
				break
			}
			if i < len(parts)-1 && !info.IsDir() {
				return nil, ErrReferenceType
			}
			if i == len(parts)-1 {
				proof.Path, proof.Info = prefix, info
			}
		}
		if !expanded {
			break
		}
	}
	if kind == "directory" && !proof.Info.IsDir() || kind == "file" && !proof.Info.Mode().IsRegular() {
		return nil, ErrReferenceType
	}
	return proof, nil
}

func (f *ReferenceFilter) checkPolicy(name, kind string, automatic bool) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		isDir := i < len(parts)-1 || kind == "directory"
		if strings.EqualFold(part, ".git") || isDir && referenceBuildDirectory(part) {
			return ErrReferenceFiltered
		}
		if automatic && (strings.HasPrefix(part, ".") || !isDir && referenceTemporaryName(part)) {
			return ErrReferenceFiltered
		}
		prefix := strings.Join(parts[:i+1], "/")
		ignored, err := f.matcher.Match(prefix, isDir)
		if err != nil {
			if f.ctx.Err() != nil {
				return f.ctx.Err()
			}
			return ErrReferenceIgnore
		}
		if ignored {
			return ErrReferenceFiltered
		}
	}
	return nil
}

func referenceBuildDirectory(name string) bool {
	switch name {
	case "node_modules", "vendor", "target", "dist", "build", "__pycache__":
		return true
	default:
		return false
	}
}

func referenceTemporaryName(name string) bool {
	return strings.HasSuffix(name, "~") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".swp") || strings.HasSuffix(name, ".swo")
}

// OpenFile 对已校验的实际目标 open；失败不会泄漏句柄。
func (f *ReferenceFilter) OpenFile(name string, automatic bool) (*os.File, *CheckedReference, error) {
	proof, err := f.Check(name, "file", automatic)
	if err != nil {
		return nil, nil, err
	}
	file, _, err := OpenRegular(f.root, proof.Path)
	if err != nil {
		return nil, nil, ErrReferenceUnavailable
	}
	if err := f.Verify(proof, file); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, proof, nil
}

// OpenDirectory 拒绝检查后被替换成链接或其它身份的目录。
func (f *ReferenceFilter) OpenDirectory(name string, automatic bool) (*os.File, *CheckedReference, error) {
	proof, err := f.Check(name, "directory", automatic)
	if err != nil {
		return nil, nil, err
	}
	file, err := OpenNonblock(f.root, proof.Path)
	if err != nil {
		return nil, nil, ErrReferenceUnavailable
	}
	if err := f.Verify(proof, file); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, proof, nil
}

// Verify 校验同一实际句柄与所有路径绑定；读后调用还能检测 size/mtime 变化。
func (f *ReferenceFilter) Verify(proof *CheckedReference, file *os.File) error {
	if err := f.ctx.Err(); err != nil {
		return err
	}
	if proof == nil || file == nil {
		return ErrReferenceChanged
	}
	actual, err := file.Stat()
	if err != nil || !sameReferenceFile(proof.Info, actual) {
		return ErrReferenceChanged
	}
	for _, binding := range proof.bindings {
		if err := f.ctx.Err(); err != nil {
			return err
		}
		current, err := f.root.Lstat(binding.path)
		if err != nil || !sameReferenceFile(binding.info, current) {
			return ErrReferenceChanged
		}
	}
	return nil
}

func sameReferenceFile(before, after os.FileInfo) bool {
	if before == nil || after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		return false
	}
	// 祖先目录新增无关条目不解除路径绑定；目录只验证身份和类型/权限。
	// 普通文件及链接仍必须验证读前后 size/mtime，不能复用已变正文。
	if before.IsDir() {
		return true
	}
	return before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
