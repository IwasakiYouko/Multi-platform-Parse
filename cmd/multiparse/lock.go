package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockDataDir(dir string) (func(), error) {
	f, e := os.OpenFile(filepath.Join(dir, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = f.Close()
		return nil, fmt.Errorf("管理数据目录已由另一服务实例使用：%w", e)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
