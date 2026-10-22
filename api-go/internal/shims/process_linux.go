//go:build linux

package shims

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func listPlatform() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	bootTime := linuxBootTime()
	var out []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Getuid() {
			continue
		}
		f, err := os.Open(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		line, readErr := bufio.NewReader(f).ReadString('\n')
		f.Close()
		if readErr != nil && len(line) == 0 {
			continue
		}
		comm, rest, ok := parseProcStat(line)
		if !ok || comm != ProcessName {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 20 {
			continue
		}
		ppid, e1 := strconv.Atoi(fields[1])
		utime, e2 := strconv.ParseUint(fields[11], 10, 64)
		stime, e3 := strconv.ParseUint(fields[12], 10, 64)
		start, e4 := strconv.ParseUint(fields[19], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		cpu := time.Duration((utime + stime) * uint64(time.Second) / 100)
		age := time.Since(time.Unix(int64(bootTime), 0).Add(time.Duration(start) * time.Second / 100))
		var rss int64
		if statm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "statm")); err == nil {
			var total, resident int64
			if _, err := fmt.Sscan(string(statm), &total, &resident); err == nil {
				rss = resident * int64(os.Getpagesize())
			}
		}
		out = append(out, Process{PID: pid, PPID: ppid, Age: age, AgeSeconds: int64(age.Seconds()), CPUS: cpu, CPUSeconds: cpu.Seconds(), RSSBytes: rss, Comm: comm})
	}
	return out, nil
}

func linuxBootTime() int64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Now().Unix()
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err == nil {
				return value
			}
		}
	}
	return time.Now().Unix()
}

func parseProcStat(line string) (string, string, bool) {
	start := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if start < 0 || end <= start {
		return "", "", false
	}
	return line[start+1 : end], strings.TrimSpace(line[end+1:]), true
}
