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

	"golang.org/x/sys/unix"
)

func processIdentity(pid int) (Process, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid))
	info, err := os.Stat(path)
	if err != nil {
		return Process{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Process{}, fmt.Errorf("process uid unavailable")
	}
	data, err := os.ReadFile(filepath.Join(path, "stat"))
	if err != nil {
		return Process{}, err
	}
	comm, rest, ok := parseProcStat(string(data))
	fields := strings.Fields(rest)
	if !ok || len(fields) < 20 {
		return Process{}, fmt.Errorf("process stat unavailable")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return Process{}, err
	}
	return Process{PID: pid, UID: int(st.Uid), StartTime: start, Comm: comm}, nil
}

func signalProcess(expected Process) error {
	return signalWithPidfd(expected, unix.PidfdOpen, unix.Close, processIdentity, func(fd int) error {
		return unix.PidfdSendSignal(fd, syscall.SIGKILL, nil, 0)
	})
}

func signalWithPidfd(expected Process, open func(int, int) (int, error), closeFD func(int) error, identify func(int) (Process, error), send func(int) error) error {
	fd, err := open(expected.PID, 0)
	if err != nil {
		return ErrProcessChanged
	}
	defer closeFD(fd)
	identity, err := identify(expected.PID)
	if err != nil || identity.PID != expected.PID || identity.UID != expected.UID || identity.StartTime != expected.StartTime || identity.Comm != ProcessName {
		return ErrProcessChanged
	}
	return send(fd)
}

func listPlatform() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	bootTime, err := linuxBootTime()
	if err != nil {
		return nil, err
	}
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
		cpuAt := time.Now()
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
		cpu := ticksDuration(utime + stime)
		startAge := ticksDuration(start)
		age := time.Since(time.Unix(bootTime, 0).Add(startAge))
		var rss int64
		if statm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "statm")); err == nil {
			var total, resident int64
			if _, err := fmt.Sscan(string(statm), &total, &resident); err == nil {
				rss = resident * int64(os.Getpagesize())
			}
		}
		out = append(out, Process{PID: pid, PPID: ppid, UID: int(st.Uid), StartTime: start, Age: age, AgeKnown: true, AgeSeconds: int64(age.Seconds()), CPUS: cpu, CPUAt: cpuAt, CPUSeconds: cpu.Seconds(), RSSBytes: rss, Comm: comm})
	}
	return out, nil
}

func linuxBootTime() (int64, error) {
	return readLinuxBootTime(os.ReadFile)
}

func readLinuxBootTime(readFile func(string) ([]byte, error)) (int64, error) {
	data, err := readFile("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("read Linux boot time: %w", err)
	}
	return parseLinuxBootTime(data)
}

func parseLinuxBootTime(data []byte) (int64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "btime ") {
			value, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err == nil && value > 0 {
				return value, nil
			}
			return 0, fmt.Errorf("invalid Linux boot time %q", line)
		}
	}
	return 0, fmt.Errorf("Linux boot time missing from /proc/stat")
}

func ticksDuration(ticks uint64) time.Duration {
	const ticksPerSecond = uint64(100)
	return time.Duration(ticks/ticksPerSecond)*time.Second + time.Duration(ticks%ticksPerSecond)*(time.Second/time.Duration(ticksPerSecond))
}

func parseProcStat(line string) (string, string, bool) {
	start := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if start < 0 || end <= start {
		return "", "", false
	}
	return line[start+1 : end], strings.TrimSpace(line[end+1:]), true
}
