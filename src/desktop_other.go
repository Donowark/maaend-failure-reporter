//go:build !windows

package main

import (
	"context"
	"errors"
)

func desktopPath() (string, error) {
	return "", errors.New("此程序面向 Windows；测试时请传入 --output-dir")
}
func fallbackPath() string { return "" }

func listenerOwner(_ context.Context, _ int) string { return "" }
