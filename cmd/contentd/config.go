package main

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
)

const defaultListenAddr = "127.0.0.1:8088"

var (
	errMissingDBPath     = errors.New("CONTENT_DB_PATH is required")
	errInvalidListenAddr = errors.New("CONTENT_LISTEN_ADDR must be a loopback IP address and valid port")
)

type config struct {
	DBPath     string
	ListenAddr string
}

// LoadConfig reads and validates the contentd environment configuration.
func LoadConfig() (config, error) {
	dbPath := strings.TrimSpace(os.Getenv("CONTENT_DB_PATH"))
	if dbPath == "" {
		return config{}, errMissingDBPath
	}

	listenAddr := strings.TrimSpace(os.Getenv("CONTENT_LISTEN_ADDR"))
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
	if !isLoopbackAddress(listenAddr) {
		return config{}, errInvalidListenAddr
	}

	return config{DBPath: dbPath, ListenAddr: listenAddr}, nil
}

func isLoopbackAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	portNumber, err := strconv.Atoi(port)
	return err == nil && portNumber > 0 && portNumber <= 65535
}
