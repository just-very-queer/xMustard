/*
 * gitspy: exact peak memory of a command and of every Git child it starts (WS-RT18,
 * critique C9). Linux carries a forking process's resident high-water mark into its
 * child's ru_maxrss across exec, so a Git child forked by xmustard-core reports the
 * core's peak, not its own. gitspy is small: a Git child forked by it starts from
 * gitspy's own few hundred KiB, so wait4's ru_maxrss is the child's own peak.
 *
 *   gitspy run <out_file> <timeout_s> <cmd> [args...]
 *     Runs cmd in its own process group with stdout to out_file and stderr discarded,
 *     and prints one JSON line: {"rc": N | "timeout", "wall_s": X, "maxrss_kb": N}.
 *     maxrss_kb is wait4's ru_maxrss of cmd: its own peak or a waited descendant's,
 *     whichever is larger. Past timeout_s the group is killed.
 *
 *   gitspy as `git` (a link named git first on PATH), with GITSPY_REAL (the real git)
 *   and GITSPY_LOG set: runs the real git with the same arguments, then appends
 *     <maxrss_kb> TAB <exit> TAB <private-index 0|1> TAB <arguments joined by spaces>
 *   to GITSPY_LOG and exits as git did. private-index is 1 when GIT_INDEX_FILE is set.
 *
 * Box only (Linux ru_maxrss is in KiB). Build: cc -O2 -o gitspy gitspy.c
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/time.h>
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

static int as_git(int argc, char **argv) {
    const char *real = getenv("GITSPY_REAL");
    const char *log = getenv("GITSPY_LOG");
    if (!real || !log) {
        fprintf(stderr, "gitspy: GITSPY_REAL and GITSPY_LOG must be set\n");
        return 127;
    }
    pid_t pid = fork();
    if (pid < 0)
        return 127;
    if (pid == 0) {
        execv(real, argv);
        _exit(127);
    }
    int status = 0;
    struct rusage ru;
    while (wait4(pid, &status, 0, &ru) < 0) {
        if (errno != EINTR)
            return 127;
    }
    char line[8192];
    int n = snprintf(line, sizeof line, "%ld\t%d\t%d\t", ru.ru_maxrss, exit_code(status),
                     getenv("GIT_INDEX_FILE") != NULL);
    for (int i = 1; i < argc && n < (int)sizeof line - 2; i++)
        n += snprintf(line + n, sizeof line - n, i > 1 ? " %s" : "%s", argv[i]);
    if (n > (int)sizeof line - 2)
        n = sizeof line - 2;
    line[n++] = '\n';
    int fd = open(log, O_WRONLY | O_APPEND | O_CREAT, 0644);
    if (fd >= 0) {
        ssize_t w = write(fd, line, n);
        (void)w;
        close(fd);
    }
    return exit_code(status);
}

static int run(int argc, char **argv) {
    if (argc < 5) {
        fprintf(stderr, "usage: gitspy run <out_file> <timeout_s> <cmd> [args...]\n");
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
        execvp(argv[4], argv + 4);
        _exit(127);
    }
    close(out);
    setpgid(pid, pid);
    int status = 0, timed_out = 0;
    struct rusage ru;
    for (;;) {
        pid_t got = wait4(pid, &status, WNOHANG, &ru);
        if (got == pid)
            break;
        if (got < 0 && errno != EINTR)
            return 2;
        if (now_s() - start > timeout) {
            kill(-pid, SIGKILL);
            while (wait4(pid, &status, 0, &ru) < 0 && errno == EINTR) {
            }
            timed_out = 1;
            break;
        }
        struct timespec nap = {0, 10 * 1000 * 1000};
        nanosleep(&nap, NULL);
    }
    if (timed_out)
        printf("{\"rc\": \"timeout\", ");
    else
        printf("{\"rc\": %d, ", exit_code(status));
    printf("\"wall_s\": %.2f, \"maxrss_kb\": %ld}\n", now_s() - start, ru.ru_maxrss);
    return 0;
}

int main(int argc, char **argv) {
    const char *slash = strrchr(argv[0], '/');
    const char *name = slash ? slash + 1 : argv[0];
    if (strcmp(name, "git") == 0)
        return as_git(argc, argv);
    if (argc >= 2 && strcmp(argv[1], "run") == 0)
        return run(argc, argv);
    fprintf(stderr, "usage: gitspy run <out_file> <timeout_s> <cmd> [args...]\n");
    return 2;
}
