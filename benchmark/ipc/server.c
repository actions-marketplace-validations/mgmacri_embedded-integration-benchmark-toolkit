/*
 * server.c — Unix domain socket server for IPC comparison benchmark
 *
 * Creates a Unix socket, accepts connections, reads messages formatted as
 * "subsystem:timestamp_ns:key1=val1|key2=val2|...\n", and dispatches through
 * the same subsystem table as the inotify watcher for fair comparison.
 *
 * Measures three latencies per message:
 *   1. Dispatch: socket delivery time (cross-process, CLOCK_REALTIME)
 *   2. Processing: parse + compare + apply time (same-process, CLOCK_MONOTONIC)
 *   3. Pipeline: total end-to-end from client timestamp to processing complete
 *
 * CLI: ./ipc_server <socket_path>
 * Runs until SIGINT/SIGTERM or client disconnect, then prints JSON to stdout.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>
#include <errno.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <sys/select.h>

#include "../common/latency.h"
#include "../common/subsystem.h"
#include "../common/bench_runtime.h"

/* ---------- Signal handling ---------- */

static volatile sig_atomic_t g_stop = 0;

static void sigint_handler(int sig) {
    (void)sig;
    g_stop = 1;
}

/* ---------- Subsystem registry ---------- */

static subsys_registry_t g_registry;
static int *g_handler_counts = NULL;

static int total_config_changes = 0;

/* ---------- Message processing ---------- */

static void process_message(const char *msg,
                            latency_array_t *dispatch_latencies,
                            latency_array_t *processing_latencies,
                            latency_array_t *pipeline_latencies) {
    struct timespec now;
    clock_gettime(CLOCK_REALTIME, &now);
    int64_t recv_time_ns = (int64_t)now.tv_sec * 1000000000LL + now.tv_nsec;

    struct timespec mono_start;
    clock_gettime(CLOCK_MONOTONIC, &mono_start);

    /* Parse "subsystem:timestamp_ns:payload" */
    char subsystem[64] = {0};
    const char *first_colon = strchr(msg, ':');
    if (!first_colon) return;

    size_t name_len = (size_t)(first_colon - msg);
    if (name_len >= sizeof(subsystem)) return;
    memcpy(subsystem, msg, name_len);
    subsystem[name_len] = '\0';

    const char *ts_start = first_colon + 1;
    const char *second_colon = strchr(ts_start, ':');
    int64_t writer_time_ns = strtoll(ts_start, NULL, 10);
    if (writer_time_ns <= 0) return;

    int32_t sub_idx = subsys_get_index(&g_registry, subsystem);
    if (sub_idx < 0) return;

    /* Delivery latency */
    int64_t dispatch_ns = recv_time_ns - writer_time_ns;
    latency_push(dispatch_latencies, dispatch_ns);

    /* Process payload if present */
    if (second_colon && *(second_colon + 1)) {
        const char *payload = second_colon + 1;
        int32_t changes = subsys_parse_and_apply(&g_registry, sub_idx, payload);
        total_config_changes += changes;
    }

    struct timespec mono_end;
    clock_gettime(CLOCK_MONOTONIC, &mono_end);
    int64_t processing_ns = (mono_end.tv_sec - mono_start.tv_sec) * 1000000000LL +
                            (mono_end.tv_nsec - mono_start.tv_nsec);
    latency_push(processing_latencies, processing_ns);

    struct timespec end_rt;
    clock_gettime(CLOCK_REALTIME, &end_rt);
    int64_t end_rt_ns = (int64_t)end_rt.tv_sec * 1000000000LL + end_rt.tv_nsec;
    int64_t pipeline_ns = end_rt_ns - writer_time_ns;
    latency_push(pipeline_latencies, pipeline_ns);

    g_handler_counts[sub_idx]++;
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
    fprintf(out, "  \"role\": \"ipc_socket_server\",\n");
    fprintf(out, "  \"total_events\": %d,\n", total);
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
    if (argc != 2) {
        fprintf(stderr, "Usage: %s <socket_path>\n", argv[0]);
        return 1;
    }

    const char *socket_path = argv[1];

    struct sigaction sa = { .sa_handler = sigint_handler };
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    /* Initialize subsystem registry */
    bench_runtime_t rt;
    if (bench_runtime_load(&rt, NULL) == 0 && rt.subsystem_count > 0) {
        const char *names[SUBSYS_MAX_SUBSYSTEMS];
        for (int32_t i = 0; i < rt.subsystem_count && i < SUBSYS_MAX_SUBSYSTEMS; i++) {
            names[i] = rt.subsystems[i].name;
        }
        subsys_init_from_names(&g_registry, names, rt.subsystem_count);
        fprintf(stderr, "[ipc_server] Runtime config: %d subsystems\n", (int)rt.subsystem_count);
    } else {
        subsys_init_defaults(&g_registry);
    }
    g_handler_counts = calloc((size_t)g_registry.count, sizeof(int));
    if (!g_handler_counts) {
        fprintf(stderr, "[ipc_server] calloc handler counts failed\n");
        return 1;
    }

    unlink(socket_path);

    int server_fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (server_fd < 0) {
        perror("[ipc_server] socket");
        return 1;
    }

    struct sockaddr_un addr;
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    strncpy(addr.sun_path, socket_path, sizeof(addr.sun_path) - 1);

    if (bind(server_fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        perror("[ipc_server] bind");
        close(server_fd);
        return 1;
    }

    if (listen(server_fd, 1) < 0) {
        perror("[ipc_server] listen");
        close(server_fd);
        return 1;
    }

    fprintf(stderr, "[ipc_server] Listening on %s\n", socket_path);

    latency_array_t dispatch_latencies;
    latency_array_t processing_latencies;
    latency_array_t pipeline_latencies;
    latency_init(&dispatch_latencies);
    latency_init(&processing_latencies);
    latency_init(&pipeline_latencies);

    int client_fd = -1;
    while (!g_stop) {
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(server_fd, &rfds);
        struct timeval tv = { .tv_sec = 0, .tv_usec = 500000 };
        int sel = select(server_fd + 1, &rfds, NULL, NULL, &tv);
        if (sel < 0) {
            if (errno == EINTR) continue;
            perror("[ipc_server] select(accept)");
            break;
        }
        if (sel == 0) continue; /* timeout — recheck g_stop */
        client_fd = accept(server_fd, NULL, NULL);
        if (client_fd < 0) {
            if (errno == EINTR) continue;
            perror("[ipc_server] accept");
            break;
        }
        break;
    }

    if (client_fd < 0) {
        close(server_fd);
        unlink(socket_path);
        print_json(stdout, &dispatch_latencies, &processing_latencies, &pipeline_latencies);
        latency_free(&dispatch_latencies);
        latency_free(&processing_latencies);
        latency_free(&pipeline_latencies);
        return 0;
    }

    fprintf(stderr, "[ipc_server] Client connected\n");

    char recv_buf[4096];
    char line_buf[1024];
    int line_pos = 0;

    while (!g_stop) {
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(client_fd, &rfds);
        struct timeval tv = { .tv_sec = 0, .tv_usec = 500000 };
        int sel = select(client_fd + 1, &rfds, NULL, NULL, &tv);
        if (sel < 0) {
            if (errno == EINTR) continue;
            perror("[ipc_server] select(read)");
            break;
        }
        if (sel == 0) continue; /* timeout — recheck g_stop */
        ssize_t n = read(client_fd, recv_buf, sizeof(recv_buf));
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            break;
        }

        for (ssize_t i = 0; i < n; i++) {
            if (recv_buf[i] == '\n') {
                line_buf[line_pos] = '\0';
                if (line_pos > 0) {
                    process_message(line_buf,
                                    &dispatch_latencies, &processing_latencies, &pipeline_latencies);
                }
                line_pos = 0;
            } else if (line_pos < (int)sizeof(line_buf) - 1) {
                line_buf[line_pos++] = recv_buf[i];
            }
        }
    }

    close(client_fd);
    close(server_fd);
    unlink(socket_path);

    print_json(stdout, &dispatch_latencies, &processing_latencies, &pipeline_latencies);

    int total = 0;
    for (int32_t i = 0; i < g_registry.count; i++) {
        total += g_handler_counts[i];
    }
    fprintf(stderr, "[ipc_server] Done: %d events, %d config changes\n", total, total_config_changes);

    latency_free(&dispatch_latencies);
    latency_free(&processing_latencies);
    latency_free(&pipeline_latencies);
    free(g_handler_counts);
    return 0;
}
