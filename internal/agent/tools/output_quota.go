package tools

import (
	"errors"
	"io/fs"
	"os"
	"sync"
)

// 只协调本进程捕获的Bash输出；不限制已授权Bash或其它主机进程自行写文件。
// 目录身份而非路径字符串使不同Set/路径别名共享剩余额度。最后一个writer
// finish后关闭root并移除entry；不缓存历史会话、不创建后台worker。
var outputQuotas = struct {
	sync.Mutex
	entries []*conversationOutputQuota
}{}

type conversationOutputQuota struct {
	mu          sync.Mutex
	root        *os.Root
	identity    fs.FileInfo
	refs        int // outputQuotas锁内
	initialized bool
	used        int64
	err         error
}

func (s *Set) acquireOutputQuota() (*conversationOutputQuota, error) {
	root, err := s.tempRoot.OpenRoot(s.conversationOutputDir())
	if err != nil {
		return nil, err
	}
	identity, err := root.Stat(".")
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	outputQuotas.Lock()
	for _, quota := range outputQuotas.entries {
		if os.SameFile(identity, quota.identity) {
			quota.refs++
			outputQuotas.Unlock()
			if err := root.Close(); err != nil {
				return nil, errors.Join(err, quota.release())
			}
			return quota, nil
		}
	}
	quota := &conversationOutputQuota{root: root, identity: identity, refs: 1}
	outputQuotas.entries = append(outputQuotas.entries, quota)
	outputQuotas.Unlock()
	return quota, nil
}

// initializeLocked只在活跃writer的同一代扫描一次；随后每次真实写入都在mu下
// 计入used，避免每个32KiB块重新遍历整个目录。os.Root.FS不跟随逃逸路径，
// WalkDir不遍历链接/FIFO等特殊文件；读取错误必须明确失败。
func (q *conversationOutputQuota) initializeLocked() error {
	if q.initialized {
		return q.err
	}
	q.initialized = true
	q.err = fs.WalkDir(q.root.FS(), ".", func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if info.Size() >= maxConversationOutputBytes-q.used {
			q.used = maxConversationOutputBytes
			return fs.SkipAll
		}
		q.used += info.Size()
		return nil
	})
	return q.err
}

// 调用者持有q.mu；磁盘部分写入即使返回错误也必须消耗额度。
func (q *conversationOutputQuota) writeLocked(file *os.File, data []byte, commandRemaining int64) (int, error) {
	remaining := min(commandRemaining, maxConversationOutputBytes-q.used)
	if remaining <= 0 {
		return 0, nil
	}
	if int64(len(data)) > remaining {
		data = data[:remaining]
	}
	n, err := file.Write(data)
	q.used += int64(n)
	return n, err
}

func (q *conversationOutputQuota) release() error {
	outputQuotas.Lock()
	q.refs--
	last := q.refs == 0
	if last {
		for i, entry := range outputQuotas.entries {
			if entry == q {
				copy(outputQuotas.entries[i:], outputQuotas.entries[i+1:])
				end := len(outputQuotas.entries) - 1
				outputQuotas.entries[end] = nil
				outputQuotas.entries = outputQuotas.entries[:end]
				break
			}
		}
	}
	outputQuotas.Unlock()
	if last {
		return q.root.Close()
	}
	return nil
}
