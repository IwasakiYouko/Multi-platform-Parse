package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// capRun preserves the pinned official Cap runtime while keeping its admin secret
// out of compose.yaml, image layers and Docker's configured environment metadata.
func capRun() error {
	path := env("CAP_ADMIN_KEY_FILE", "/secrets/cap-admin.key")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		b, e := os.ReadFile(path)
		if e == nil {
			key := strings.TrimSpace(string(b))
			if len(key) < 32 {
				return errors.New("Cap管理密钥不足32字符")
			}
			binary, e := exec.LookPath("bun")
			if e != nil {
				return e
			}
			environment := make([]string, 0, len(os.Environ())+1)
			for _, v := range os.Environ() {
				if !strings.HasPrefix(v, "ADMIN_KEY=") {
					environment = append(environment, v)
				}
			}
			environment = append(environment, "ADMIN_KEY="+key)
			return syscall.Exec(binary, []string{binary, "run", "./src/index.js"}, environment)
		}
		if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
