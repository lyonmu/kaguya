package global

import (
	"testing"

	pkgid "github.com/lyonmu/gopkg/id"
)

func TestSonySnowFlake(t *testing.T) {

	gen, err := pkgid.NewSonySnowFlake(func() (int, error) {
		return 1, nil // 每台机器应使用不同的 ID
	})
	if err != nil {

		println(err)
	}

	newId, err := gen.GenID()
	if err != nil {

		println(err)
	}

	println(newId)

}
