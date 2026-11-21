package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
)

func TestWindowsReportsListeningPID(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	owner := listenerOwner(context.Background(), l.Addr().(*net.TCPAddr).Port)
	if !strings.Contains(owner, fmt.Sprintf("(PID %d)", os.Getpid())) {
		t.Fatal(owner)
	}
}
