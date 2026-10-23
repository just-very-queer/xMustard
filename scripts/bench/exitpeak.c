/*
 * exitpeak: the peak RSS of one short-lived command, read at its exit (D-08's line for
 * the Cursor hook client, cursor_client_rss.py).
 *
 *   exitpeak run <out_file> <timeout_s> <cmd> [args...]
 *     Runs cmd in its own process group with stdin inherited, stdout to out_file and
 *     stderr discarded. cmd is traced only to stop it at exit (PTRACE_O_TRACEEXIT),
 *     before the kernel frees its memory; there exitpeak reads VmHWM from
 *     /proc/<pid>/status. Prints one JSON line:
 *       {"rc": N | "timeout", "wall_s": X, "exit_hwm_kb": N, "maxrss_kb": N}
 *     exit_hwm_kb is -1 when cmd never reached the exit stop. Past timeout_s the
 *     group is killed.
 *
 * Why both numbers: since Linux 6.2 a process's RSS counters are per-CPU, and the high
 * water that wait4 reports is computed from them without summing, so ru_maxrss can read
 * a few hundred KiB under the page tables (the box, kernel 7.0: 2,244 KiB against 2,480
 * in smaps_rollup). Current kernels sum them for /proc's VmRSS (on the box it equals
 * smaps_rollup's Rss), and VmHWM is the larger of VmRSS and the stored high water, so at
 * the exit stop it is exact for a process whose memory only grows. maxrss_kb (wait4)
 * still sees a peak freed before exit, and it also holds this runner's pre-exec copy,
 * carried across exec. The caller takes the larger.
 *
 * Linux only (ru_maxrss in KiB). Build: cc -O2 -o exitpeak exitpeak.c
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ptrace.h>
#include <sys/resource.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static double now_s(void) {
    struct timespec t;
    clock_gettime(CLOCK_MONOTONIC, &t);
    return t.tv_sec + t.tv_nsec / 1e9;
}

static int exit_code(int status) {
    if (WIFEXITED(status))
        return WEXITSTATUS(status);
    if (WIFSIGNALED(status))
        return 128 + WTERMSIG(status);
    return 255;
}

/* VmHWM of a stopped process in KiB, or -1. */
static long vm_hwm_kb(pid_t pid) {
    char path[64], buf[8192];
    snprintf(path, sizeof path, "/proc/%d/status", (int)pid);
    int fd = open(path, O_RDONLY);
    if (fd < 0)
        return -1;
    size_t len = 0;
    ssize_t n;
    while (len < sizeof buf - 1 && (n = read(fd, buf + len, sizeof buf - 1 - len)) > 0)
        len += n;
    close(fd);
    buf[len] = 0;
    char *line = strstr(buf, "\nVmHWM:");
    return line ? strtol(line + 7, NULL, 10) : -1;
}

static int run(int argc, char **argv) {
    if (argc < 5) {
        fprintf(stderr, "usage: exitpeak run <out_file> <timeout_s> <cmd> [args...]\n");
        return 2;
    }
    double timeout = atof(argv[3]);
    int out = open(argv[2], O_WRONLY | O_CREAT | O_TRUNC, 0644);
    if (out < 0) {
        perror(argv[2]);
        return 2;
    }
    double start = now_s();
    pid_t pid = fork();
    if (pid < 0)
        return 2;
    if (pid == 0) {
        setpgid(0, 0);
        dup2(out, 1);
        int null = open("/dev/null", O_WRONLY);
        if (null >= 0)
            dup2(null, 2);
        if (ptrace(PTRACE_TRACEME, 0, NULL, NULL) != 0)
            _exit(126);
        execvp(argv[4], argv + 4);
        _exit(127);
    }
    close(out);
    setpgid(pid, pid);
    int status = 0, timed_out = 0, armed = 0;
    long hwm = -1;
    struct rusage ru;
    for (;;) {
        pid_t got = wait4(pid, &status, WNOHANG, &ru);
        if (got < 0) {
            if (errno == EINTR)
                continue;
            return 2;
        }
        if (got == 0) {
            if (!timed_out && now_s() - start > timeout) {
                kill(-pid, SIGKILL);
                timed_out = 1;
            }
            struct timespec nap = {0, 1000 * 1000};
            nanosleep(&nap, NULL);
            continue;
        }
        if (WIFEXITED(status) || WIFSIGNALED(status))
            break;
        /* A stop: the trap after exec arms the exit stop, the exit stop is read, and
           any other signal is passed on. */
        int sig = WSTOPSIG(status);
        if (!armed && sig == SIGTRAP) {
            armed = ptrace(PTRACE_SETOPTIONS, pid, NULL, (void *)(long)PTRACE_O_TRACEEXIT) == 0;
            sig = 0;
        } else if (status >> 8 == (SIGTRAP | (PTRACE_EVENT_EXIT << 8))) {
            hwm = vm_hwm_kb(pid);
            sig = 0;
        }
        ptrace(PTRACE_CONT, pid, NULL, (void *)(long)sig);
    }
    if (timed_out)
        printf("{\"rc\": \"timeout\", ");
    else
        printf("{\"rc\": %d, ", exit_code(status));
    printf("\"wall_s\": %.3f, \"exit_hwm_kb\": %ld, \"maxrss_kb\": %ld}\n", now_s() - start, hwm,
           ru.ru_maxrss);
    return 0;
}

int main(int argc, char **argv) {
    if (argc >= 2 && strcmp(argv[1], "run") == 0)
        return run(argc, argv);
    fprintf(stderr, "usage: exitpeak run <out_file> <timeout_s> <cmd> [args...]\n");
    return 2;
}
