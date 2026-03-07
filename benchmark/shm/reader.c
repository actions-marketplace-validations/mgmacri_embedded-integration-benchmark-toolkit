/*
 * reader.c — Shared memory (mmap + FIFO) consumer for benchmark
 *
 * Creates a POSIX shared memory segment and a named FIFO, then blocks on FIFO
 * reads. When the Go writer signals by writing a byte to the FIFO, reads the
 * message from the shared memory region and dispatches through the same
 * subsystem table as watcher.c and server.c for fair comparison.
 *
 * Measures three latencies per message:
 *   1. Dispatch: shm delivery time (cross-process, CLOCK_REALTIME)
 *   2. Processing: parse + compare + apply time (same-process, CLOCK_MONOTONIC)
 *   3. Pipeline: total end-to-end from writer timestamp to processing complete
 *
 * CLI: ./shm_reader
 * Runs until SIGINT/SIGTERM, then prints JSON summary to stdout.
 *
 * Setup:
 *   Reader creates /dev/shm/bench_shm (via shm_open) and a FIFO at
 *   /tmp/bench_shm_fifo (via mkfifo). The Go writer opens the same FIFO
 *   for writing.
 *
 * Memory ordering (ARMv7 — weakly ordered):
 *   Writer: populate fields → atomic_store(&ready, seq, RELEASE) → write(fifo, 1)
 *   Reader: read(fifo, 1) → atomic_load(&ready, ACQUIRE) → read fields →
 *           atomic_store(&ready, 0, RELEASE)
 *
 * Link flags: -lrt -lpthread
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>
#include <errno.h>
#include <fcntl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <stdatomic.h>

#include "shm_common.h"

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
static int sequence_errors      = 0;

/* ---------- Message processing ---------- */

static void process_message(const shm_message_t *local,
                            latency_array_t *dispatch_latencies,
                            latency_array_t *processing_latencies,
                            latency_array_t *pipeline_latencies) {
    /* Record receive time — CLOCK_REALTIME for cross-process comparison */
    struct timespec now;
    clock_gettime(CLOCK_REALTIME, &now);
    int64_t recv_time_ns = (int64_t)now.tv_sec * 1000000000LL + now.tv_nsec;

    struct timespec mono_start;
    clock_gettime(CLOCK_MONOTONIC, &mono_start);

    uint32_t sub_id = local->subsystem_id;
    if (sub_id >= (uint32_t)g_registry.count) return;

    g_handler_counts[sub_id]++;

    /* Dispatch latency: writer timestamp to reader receive */
    int64_t writer_time_ns = local->timestamp_ns;
    if (writer_time_ns <= 0) return;

    int64_t dispatch_ns = recv_time_ns - writer_time_ns;
    latency_push(dispatch_latencies, dispatch_ns);

    /* Process config payload */
    if (local->payload_length > 0 && local->payload_length < SHM_MAX_PAYLOAD) {
        int32_t changes = subsys_parse_and_apply(&g_registry, (int32_t)sub_id, local->payload);
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
    fprintf(out, "  \"role\": \"shm_reader\",\n");
    fprintf(out, "  \"total_events\": %d,\n", total);
    fprintf(out, "  \"sequence_errors\": %d,\n", sequence_errors);
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

int main(void) {
    /* Signal handling */
    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_handler = sigint_handler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    /* Initialize subsystem registry */
    bench_runtime_t brt;
    if (bench_runtime_load(&brt, NULL) == 0 && brt.subsystem_count > 0) {
        const char *names[SUBSYS_MAX_SUBSYSTEMS];
        for (int32_t i = 0; i < brt.subsystem_count && i < SUBSYS_MAX_SUBSYSTEMS; i++) {
            names[i] = brt.subsystems[i].name;
        }
        subsys_init_from_names(&g_registry, names, brt.subsystem_count);
        fprintf(stderr, "[shm_reader] Runtime config: %d subsystems\n", (int)brt.subsystem_count);
    } else {
        subsys_init_defaults(&g_registry);
    }
    g_handler_counts = calloc((size_t)g_registry.count, sizeof(int));
    if (!g_handler_counts) {
        fprintf(stderr, "[shm_reader] calloc handler counts failed\n");
        return 1;
    }

    /* Create shared memory segment */
    int shm_fd = shm_open(SHM_NAME, O_CREAT | O_RDWR, 0666);
    if (shm_fd < 0) {
        fprintf(stderr, "[shm_reader] shm_open(%s) failed: %s\n",
                SHM_NAME, strerror(errno));
        return 1;
    }

    if (ftruncate(shm_fd, SHM_SIZE) < 0) {
        fprintf(stderr, "[shm_reader] ftruncate failed: %s\n", strerror(errno));
        close(shm_fd);
        shm_unlink(SHM_NAME);
        return 1;
    }

    void *shm_ptr = mmap(NULL, SHM_SIZE, PROT_READ | PROT_WRITE,
                         MAP_SHARED, shm_fd, 0);
    close(shm_fd);  /* fd no longer needed after mmap */
    if (shm_ptr == MAP_FAILED) {
        fprintf(stderr, "[shm_reader] mmap failed: %s\n", strerror(errno));
        shm_unlink(SHM_NAME);
        return 1;
    }

    /* Zero out shared memory — ready=0 means empty */
    memset(shm_ptr, 0, SHM_SIZE);

    shm_message_t *msg = (shm_message_t *)shm_ptr;

    /* Create FIFO for signaling (remove stale first) */
    unlink(SHM_FIFO_PATH);
    if (mkfifo(SHM_FIFO_PATH, 0666) < 0) {
        fprintf(stderr, "[shm_reader] mkfifo(%s) failed: %s\n",
                SHM_FIFO_PATH, strerror(errno));
        munmap(shm_ptr, SHM_SIZE);
        shm_unlink(SHM_NAME);
        return 1;
    }

    fprintf(stderr, "[shm_reader] Ready: shm=%s fifo=%s\n",
            SHM_NAME, SHM_FIFO_PATH);

    /* Open FIFO for reading (blocks until writer opens for writing) */
    int fifo_fd = open(SHM_FIFO_PATH, O_RDONLY);
    if (fifo_fd < 0) {
        fprintf(stderr, "[shm_reader] open fifo failed: %s\n", strerror(errno));
        unlink(SHM_FIFO_PATH);
        munmap(shm_ptr, SHM_SIZE);
        shm_unlink(SHM_NAME);
        return 1;
    }

    /* Initialize latency arrays */
    latency_array_t dispatch_latencies;
    latency_array_t processing_latencies;
    latency_array_t pipeline_latencies;
    latency_init(&dispatch_latencies);
    latency_init(&processing_latencies);
    latency_init(&pipeline_latencies);

    uint32_t last_seq = 0;

    /* Main loop: block on FIFO read, then read from shared memory */
    while (!g_stop) {
        uint8_t sig_byte;
        ssize_t n = read(fifo_fd, &sig_byte, 1);
        if (n <= 0) {
            if (n == 0) break;  /* writer closed FIFO */
            if (errno == EINTR) continue;
            fprintf(stderr, "[shm_reader] fifo read failed: %s\n",
                    strerror(errno));
            break;
        }
        uint32_t ready_seq = __atomic_load_n(&msg->ready, __ATOMIC_ACQUIRE);
        if (ready_seq == 0) continue;  /* spurious wakeup */

        /* Copy message to local buffer under acquire ordering */
        shm_message_t local;
        memcpy(&local, msg, sizeof(shm_message_t));

        /* Null-terminate payload for safety */
        if (local.payload_length >= SHM_MAX_PAYLOAD) {
            local.payload_length = SHM_MAX_PAYLOAD - 1;
        }
        local.payload[local.payload_length] = '\0';

        /* Check sequence continuity */
        if (last_seq > 0 && local.sequence != last_seq + 1) {
            sequence_errors++;
        }
        last_seq = local.sequence;

        /* Clear ready flag so writer knows buffer is free */
        __atomic_store_n(&msg->ready, 0, __ATOMIC_RELEASE);

        /* Process the message */
        process_message(&local,
                        &dispatch_latencies, &processing_latencies, &pipeline_latencies);
    }

    /* Cleanup */
    close(fifo_fd);
    munmap(shm_ptr, SHM_SIZE);
    shm_unlink(SHM_NAME);
    unlink(SHM_FIFO_PATH);

    /* Output JSON results */
    print_json(stdout, &dispatch_latencies, &processing_latencies, &pipeline_latencies);

    int total = 0;
    for (int32_t i = 0; i < g_registry.count; i++) {
        total += g_handler_counts[i];
    }
    fprintf(stderr, "[shm_reader] Done: %d events, %d config changes, %d sequence errors\n",
            total, total_config_changes, sequence_errors);

    latency_free(&dispatch_latencies);
    latency_free(&processing_latencies);
    latency_free(&pipeline_latencies);
    free(g_handler_counts);

    return 0;
}
