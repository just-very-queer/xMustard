//go:build unix

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var processIdentity = apiProcessIdentity

type apiOwner struct {
	PID       int    `json:"pid"`
	StartTime int64  `json:"start_time"`
	DataDir   string `json:"data_dir"`
	Port      int    `json:"port"`
}

func apiOwnerMatches(dataDir string, port int, healthDataDirID string) bool {
	_, ok := matchingAPIOwner(dataDir, port, healthDataDirID)
	return ok
}

func matchingAPIOwner(dataDir string, port int, healthDataDirID string) (apiOwner, bool) {
	wantDir, err := filepath.Abs(dataDir)
	if err != nil {
		return apiOwner{}, false
	}
	wantDir, err = filepath.EvalSymlinks(wantDir)
	if err != nil || healthDataDirID == "" || healthDataDirID != dataDirID(wantDir) {
		return apiOwner{}, false
	}
	path := filepath.Join(wantDir, "api.owner")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return apiOwner{}, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return apiOwner{}, false
	}
	var owner apiOwner
	if json.Unmarshal(b, &owner) != nil || owner.PID <= 0 || owner.StartTime <= 0 ||
		filepath.Clean(owner.DataDir) != wantDir || owner.Port != port {
		return apiOwner{}, false
	}
	start, name, ok := processIdentity(owner.PID)
	if !ok || start != owner.StartTime || name != "xmustard-api" {
		return apiOwner{}, false
	}
	return owner, true
}

func dataDirID(dataDir string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return hex.EncodeToString(sum[:])
}

func parseLinuxProcStat(b []byte) (int64, string, bool) {
	i, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if i < 0 || end <= i {
		return 0, "", false
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" {
		return 0, "", false
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	return start, string(b[i+1 : end]), err == nil
}
