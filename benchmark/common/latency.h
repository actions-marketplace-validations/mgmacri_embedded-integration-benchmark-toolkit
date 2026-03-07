/*
 * latency.h — Shared latency measurement utilities for C benchmark programs
 *
 * Provides the latency_array_t dynamic array and percentile calculation used
 * by all C benchmark binaries (c_writer, watcher, server, shm_reader).
 *
 * This header extracts the previously copy-pasted latency code into a single
 * reusable source. Existing binaries can include this instead of duplicating
 * the implementation.
 *
 * Percentile algorithm: floor-index on sorted ascending array.
 *   L[floor(N * pct)]
 * This matches the Go implementation and satisfies copilot-instructions.md.
 *
 * All functions are static inline to avoid link-time symbol conflicts when
 * included from multiple translation units in the same binary.
 */

#ifndef BENCH_LATENCY_H
#define BENCH_LATENCY_H

#include <stdio.h>
#include <stdlib.h>
#include <stdint.h>
#include <string.h>

/* ---------- Latency array (dynamic, doubling capacity) ---------- */

typedef struct {
    int64_t *data;
    size_t   len;
    size_t   cap;
} latency_array_t;

static inline void latency_init(latency_array_t *la) {
    la->cap = 4096;
    la->len = 0;
    la->data = (int64_t *)malloc(la->cap * sizeof(int64_t));
    if (!la->data) {
        fprintf(stderr, "[latency] malloc failed for initial capacity\n");
        exit(1);
    }
}

static inline void latency_push(latency_array_t *la, int64_t val) {
    if (la->len >= la->cap) {
        la->cap *= 2;
        int64_t *tmp = (int64_t *)realloc(la->data, la->cap * sizeof(int64_t));
        if (!tmp) {
            fprintf(stderr, "[latency] realloc failed at capacity %zu\n", la->cap);
            exit(1);
        }
        la->data = tmp;
    }
    la->data[la->len++] = val;
}

static inline int latency_cmp_int64(const void *a, const void *b) {
    int64_t va = *(const int64_t *)a;
    int64_t vb = *(const int64_t *)b;
    return (va > vb) - (va < vb);
}

static inline void latency_sort(latency_array_t *la) {
    if (la->len > 0) {
        qsort(la->data, la->len, sizeof(int64_t), latency_cmp_int64);
    }
}

/*
 * Floor-index percentile: L[floor(N * pct)]
 * N = la->len, pct in [0.0, 1.0)
 * Matches the Go percentile() implementation.
 */
static inline int64_t latency_percentile(latency_array_t *la, double pct) {
    if (la->len == 0) return 0;
    size_t idx = (size_t)(la->len * pct);
    if (idx >= la->len) idx = la->len - 1;
    return la->data[idx];
}

static inline void latency_free(latency_array_t *la) {
    free(la->data);
    la->data = NULL;
    la->len = la->cap = 0;
}

/* ---------- Convenience: compute all standard percentiles ---------- */

typedef struct {
    int64_t min;
    int64_t p50;
    int64_t p95;
    int64_t p99;
    int64_t max;
} latency_stats_t;

static inline latency_stats_t latency_compute_stats(latency_array_t *la) {
    latency_stats_t s = {0, 0, 0, 0, 0};
    if (la->len == 0) return s;

    latency_sort(la);
    s.min = la->data[0];
    s.p50 = latency_percentile(la, 0.50);
    s.p95 = latency_percentile(la, 0.95);
    s.p99 = latency_percentile(la, 0.99);
    s.max = la->data[la->len - 1];
    return s;
}

#endif /* BENCH_LATENCY_H */
