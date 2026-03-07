/*
 * watcher.c — inotify sentinel file watcher for notification benchmark
 *
 * Watches a directory for IN_MOVED_TO events (atomic rename from the writer).
 * Reads the writer's CLOCK_REALTIME timestamp from file content, computes
 * dispatch latency. Then parses the config payload (pipe-separated key=value
 * pairs), compares to cached state, and updates the cache.
 *
 * Measures three latencies per event:
 *   1. Dispatch: notification delivery time (cross-process, CLOCK_REALTIME)
 *   2. Processing: parse + compare + apply time (same-process, CLOCK_MONOTONIC)
 *   3. Pipeline: total end-to-end from writer timestamp to processing complete
 *
 * CLI: ./watcher <watch_dir> [--delay-start-ms N]
 * Runs until SIGINT/SIGTERM, then prints JSON summary to stdout.
 *
 * Options:
 *   --delay-start-ms N   Wait N ms after IN_MOVED_TO before reading the file.
 *                        Simulates a slow reader; used to test fsync vs rename
 *                        race conditions. Default: 0 (no delay).
 *
 * Reliability counters (reported in JSON output):
 *   overflow_events — IN_Q_OVERFLOW events from the inotify kernel queue.
 *                     Non-zero means the kernel dropped notifications.
 *   coalesced_events — Writer sequence numbers that were skipped, indicating
 *                      the kernel coalesced multiple writes to the same file.
 *
 * Customize: Change the subsystem names and dispatch table to match your
 * application's config domains.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <sys/inotify.h>
#include <sys/stat.h>
#include <limits.h>

#include "../common/latency.h"
#include "../common/subsystem.h"
#include "../common/bench_runtime.h"

/* ---------- Signal handling ---------- */

static volatile sig_atomic_t g_stop = 0;

static void sigint_handler(int sig) {
    (void)sig;
    g_stop = 1;
}

/* ---------- Subsystem registry (populated from runtime config or defaults) ---------- */

static subsys_registry_t g_registry;

/* Per-subsystem event counters */
static int *g_handler_counts = NULL;

static int missed_events        = 0;
static int unknown_events       = 0;
static int total_config_changes = 0;
static int overflow_events      = 0;
static int coalesced_events     = 0;
static uint32_t last_sequence   = 0;
static int delay_start_ms       = 0;

/* ---------- Event handler ---------- */

static void handle_event(const char *watch_dir, const char *filename,
                         latency_array_t *dispatch_latencies,
                         latency_array_t *processing_latencies,
                         latency_array_t *pipeline_latencies) {
    /* Record handler entry time — CLOCK_REALTIME for cross-process comparison */
    struct timespec now;
    clock_gettime(CLOCK_REALTIME, &now);
    int64_t handler_time_ns = (int64_t)now.tv_sec * 1000000000LL + now.tv_nsec;

    /* Optional delay to simulate slow reader (for fsync race testing) */
    if (delay_start_ms > 0) {
        struct timespec delay = {
            .tv_sec  = delay_start_ms / 1000,
            .tv_nsec = (delay_start_ms % 1000) * 1000000L
        };
        nanosleep(&delay, NULL);
    }

    struct timespec mono_start;
    clock_gettime(CLOCK_MONOTONIC, &mono_start);

    int32_t sub_idx = subsys_get_index(&g_registry, filename);
    if (sub_idx < 0) {
        unknown_events++;
        return;
    }

    char path[PATH_MAX];
    snprintf(path, sizeof(path), "%s/%s", watch_dir, filename);

    int fd = open(path, O_RDONLY);
    if (fd < 0) {
        missed_events++;
        return;
    }

    char content[1024] = {0};
    ssize_t n = read(fd, content, sizeof(content) - 1);
    close(fd);

    if (n <= 0) {
        missed_events++;
        return;
    }
    content[n] = '\0';

    /* Parse file: first line = timestamp, optional second line = sequence,
     * rest = config payload */
    char *newline = strchr(content, '\n');
    if (!newline) {
        /* Simple format: content is just the timestamp */
        int64_t writer_time_ns = strtoll(content, NULL, 10);
        if (writer_time_ns > 0) {
            int64_t latency_ns = handler_time_ns - writer_time_ns;
            latency_push(dispatch_latencies, latency_ns);
            latency_push(pipeline_latencies, latency_ns);
            g_handler_counts[sub_idx]++;
        } else {
            missed_events++;
        }
        unlink(path);
        return;
    }

    *newline = '\0';
    int64_t writer_time_ns = strtoll(content, NULL, 10);
    if (writer_time_ns <= 0) {
        missed_events++;
        unlink(path);
        return;
    }

    /* Dispatch latency: notification delivery only */
    int64_t dispatch_ns = handler_time_ns - writer_time_ns;
    latency_push(dispatch_latencies, dispatch_ns);

    /* Check for sequence number (second line, if present) for coalescing detection */
    const char *payload = newline + 1;
    char *second_newline = strchr(payload, '\n');
    if (second_newline) {
        uint32_t seq = (uint32_t)strtoul(payload, NULL, 10);
        if (seq > 0 && last_sequence > 0 && seq > last_sequence + 1) {
            coalesced_events += (int)(seq - last_sequence - 1);
        }
        if (seq > 0) {
            last_sequence = seq;
        }
        payload = second_newline + 1;
    }
    if (sub_idx >= 0) {
        int32_t changes = subsys_parse_and_apply(&g_registry, sub_idx, payload);
        total_config_changes += changes;
    }

    /* Processing time (same-process, CLOCK_MONOTONIC) */
    struct timespec mono_end;
    clock_gettime(CLOCK_MONOTONIC, &mono_end);
    int64_t processing_ns = (mono_end.tv_sec - mono_start.tv_sec) * 1000000000LL +
                            (mono_end.tv_nsec - mono_start.tv_nsec);
    latency_push(processing_latencies, processing_ns);

    /* Total pipeline: writer timestamp to processing complete */
    struct timespec end_rt;
    clock_gettime(CLOCK_REALTIME, &end_rt);
    int64_t end_rt_ns = (int64_t)end_rt.tv_sec * 1000000000LL + end_rt.tv_nsec;
    int64_t pipeline_ns = end_rt_ns - writer_time_ns;
    latency_push(pipeline_latencies, pipeline_ns);

    g_handler_counts[sub_idx]++;
    unlink(path);
}

/* ---------- JSON output ---------- */

static void print_latency_stats(FILE *out, const char *name, latency_array_t *la, int trailing_comma) {
    latency_sort(la);
    int64_t mn  = (la->len > 0) ? la->data[0] : 0;
    int64_t p50 = latency_percentile(la, 0.50);
    int64_t p95 = latency_percentile(la, 0.95);
    int64_t p99 = latency_percentile(la, 0.99);
    int64_t mx  = (la->len > 0) ? la->data[la->len - 1] : 0;

    fprintf(out, "  \"%s\": {\n", name);
    fprintf(out, "    \"min\": %ld,\n", (long)mn);
    fprintf(out, "    \"p50\": %ld,\n", (long)p50);
    fprintf(out, "    \"p95\": %ld,\n", (long)p95);
    fprintf(out, "    \"p99\": %ld,\n", (long)p99);
    fprintf(out, "    \"max\": %ld\n",  (long)mx);
    fprintf(out, "  }%s\n", trailing_comma ? "," : "");
}

static void print_json(FILE *out,
                       latency_array_t *dispatch_la,
                       latency_array_t *processing_la,
                       latency_array_t *pipeline_la) {
    int total = 0;
    for (int32_t i = 0; i < g_registry.count; i++) {
        total += g_handler_counts[i];
    }

    fprintf(out, "{\n");
    fprintf(out, "  \"role\": \"inotify_watcher\",\n");
    fprintf(out, "  \"total_events\": %d,\n", total);
    fprintf(out, "  \"missed_events\": %d,\n", missed_events);
    fprintf(out, "  \"unknown_events\": %d,\n", unknown_events);
    fprintf(out, "  \"overflow_events\": %d,\n", overflow_events);
    fprintf(out, "  \"coalesced_events\": %d,\n", coalesced_events);
    fprintf(out, "  \"config_changes_detected\": %d,\n", total_config_changes);

    print_latency_stats(out, "dispatch_latency_ns", dispatch_la, 1);
    print_latency_stats(out, "processing_latency_ns", processing_la, 1);
    print_latency_stats(out, "total_pipeline_latency_ns", pipeline_la, 1);

    fprintf(out, "  \"events_by_subsystem\": {\n");
    for (int32_t i = 0; i < g_registry.count; i++) {
        fprintf(out, "    \"%s\": %d", g_registry.entries[i].name, g_handler_counts[i]);
        if (i + 1 < g_registry.count) {
            fprintf(out, ",");
        }
        fprintf(out, "\n");
    }
    fprintf(out, "  }\n");
    fprintf(out, "}\n");
}

/* ---------- Main ---------- */

int main(int argc, char *argv[]) {
    if (argc < 2) {
        fprintf(stderr, "Usage: %s <watch_dir> [--delay-start-ms N]\n", argv[0]);
        return 1;
    }

    const char *watch_dir = argv[1];

    /* Parse optional CLI flags */
    for (int i = 2; i < argc; i++) {
        if (strcmp(argv[i], "--delay-start-ms") == 0 && i + 1 < argc) {
            delay_start_ms = atoi(argv[++i]);
            fprintf(stderr, "[watcher] Delay start: %d ms\n", delay_start_ms);
        }
    }

    struct sigaction sa = { .sa_handler = sigint_handler };
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    /* Initialize subsystem registry from runtime config or defaults */
    bench_runtime_t rt;
    if (bench_runtime_load(&rt, NULL) == 0 && rt.subsystem_count > 0) {
        const char *names[SUBSYS_MAX_SUBSYSTEMS];
        for (int32_t i = 0; i < rt.subsystem_count && i < SUBSYS_MAX_SUBSYSTEMS; i++) {
            names[i] = rt.subsystems[i].name;
        }
        subsys_init_from_names(&g_registry, names, rt.subsystem_count);
        fprintf(stderr, "[watcher] Runtime config: %d subsystems\n", (int)rt.subsystem_count);
    } else {
        subsys_init_defaults(&g_registry);
    }
    g_handler_counts = calloc((size_t)g_registry.count, sizeof(int));
    if (!g_handler_counts) {
        fprintf(stderr, "[watcher] calloc handler counts failed\n");
        return 1;
    }

    mkdir(watch_dir, 0755);

    int inotify_fd = inotify_init();
    if (inotify_fd < 0) {
        perror("[watcher] inotify_init");
        return 1;
    }

    int watch_fd = inotify_add_watch(inotify_fd, watch_dir, IN_MOVED_TO | IN_Q_OVERFLOW);
    if (watch_fd < 0) {
        perror("[watcher] inotify_add_watch");
        close(inotify_fd);
        return 1;
    }

    fprintf(stderr, "[watcher] Watching %s for IN_MOVED_TO events\n", watch_dir);

    latency_array_t dispatch_latencies;
    latency_array_t processing_latencies;
    latency_array_t pipeline_latencies;
    latency_init(&dispatch_latencies);
    latency_init(&processing_latencies);
    latency_init(&pipeline_latencies);

    char buf[sizeof(struct inotify_event) + NAME_MAX + 1]
        __attribute__((aligned(__alignof__(struct inotify_event))));

    while (!g_stop) {
        ssize_t len = read(inotify_fd, buf, sizeof(buf));
        if (len < 0) {
            if (errno == EINTR) continue;
            perror("[watcher] read");
            break;
        }

        for (char *ptr = buf; ptr < buf + len; ) {
            struct inotify_event *event = (struct inotify_event *)ptr;

            if (event->mask & IN_Q_OVERFLOW) {
                overflow_events++;
                fprintf(stderr, "[watcher] WARNING: IN_Q_OVERFLOW — kernel dropped events\n");
            } else if (event->len > 0 && event->name[0] != '.') {
                handle_event(watch_dir, event->name,
                             &dispatch_latencies, &processing_latencies, &pipeline_latencies);
            }

            ptr += sizeof(struct inotify_event) + event->len;
        }
    }

    inotify_rm_watch(inotify_fd, watch_fd);
    close(inotify_fd);

    print_json(stdout, &dispatch_latencies, &processing_latencies, &pipeline_latencies);

    int total_processed = 0;
    for (int32_t i = 0; i < g_registry.count; i++) {
        total_processed += g_handler_counts[i];
    }
    fprintf(stderr, "[watcher] Done: %d events, %d config changes, %d missed, %d unknown, %d overflow, %d coalesced\n",
            total_processed, total_config_changes, missed_events, unknown_events,
            overflow_events, coalesced_events);

    latency_free(&dispatch_latencies);
    latency_free(&processing_latencies);
    latency_free(&pipeline_latencies);
    free(g_handler_counts);

    return 0;
}
