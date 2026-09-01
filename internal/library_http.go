package internal

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// libraryCompanionHTTPBase maps a module discovery dial address to the companion
// HTTP API base URL. MuxCore library modules conventionally bind gRPC on N and
// health/REST on N+1 (e.g. media-books :9650 / :9651).
func libraryCompanionHTTPBase(dialAddr string) (string, error) {
	host, portStr, err := net.SplitHostPort(dialAddr)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", fmt.Errorf("parse port %q: %w", portStr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port+1)), nil
}

func (m *Module) libraryHTTPBase(ctx context.Context, capability string) (string, error) {
	if m.testLibraryHTTP != nil {
		if base, ok := m.testLibraryHTTP[capability]; ok {
			return base, nil
		}
	}
	addr, err := m.findModuleByCapability(ctx, capability)
	if err != nil {
		return "", err
	}
	base, err := libraryCompanionHTTPBase(addr)
	if err != nil {
		return "", fmt.Errorf("%s http base: %w", capability, err)
	}
	return base, nil
}
