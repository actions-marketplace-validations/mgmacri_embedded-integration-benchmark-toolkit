/*
 * shm_common.h — Shared memory layout for mmap + FIFO benchmark
 *
 * This header defines the single source of truth for the shared memory
 * message structure exchanged between the Go writer (producer) and
 * C reader (consumer). The Go writer uses matching byte offsets
 * documented below.
 *
 * Transport: POSIX shared memory (shm_open + mmap) + named FIFO for signaling
 *
 * IMPORTANT: All types are fixed-width for ARM/x86 portability.
 * Do NOT use int, long, size_t, or bool in this struct.
 *
 * Memory ordering (ARMv7):
 *   Writer: populate all fields → __atomic_store_n(&ready, seq, __ATOMIC_RELEASE)
 *            → write(fifo, &byte, 1)
 *   Reader: read(fifo, &byte, 1) → __atomic_load_n(&ready, __ATOMIC_ACQUIRE)
 *            → read all fields
 */

#ifndef SHM_COMMON_H
#define SHM_COMMON_H

#include <stdint.h>

/*
 * Shared memory segment name and size.
 * shm_open("/bench_shm") maps to /dev/shm/bench_shm on Linux.
 */
#define SHM_NAME       "/bench_shm"
#define SHM_SIZE       4096   /* one page — more than enough for our message */
#define SHM_MAX_PAYLOAD 512   /* >= 350 bytes for largest config payload */

/*
 * FIFO (named pipe) path for signaling between writer and reader.
 * The C reader creates this FIFO; the Go writer opens it for writing.
 * One byte written per message signals the reader to consume.
 */
#define SHM_FIFO_PATH  "/tmp/bench_shm_fifo"

/*
 * shm_message_t — layout of the shared memory region
 *
 * Byte offsets (verified by static_assert below):
 *   offset  0: ready          (uint32_t, atomic — sequence number when ready)
 *   offset  4: sequence       (uint32_t, monotonic sequence counter)
 *   offset  8: timestamp_ns   (int64_t, CLOCK_REALTIME nanoseconds from writer)
 *   offset 16: payload_length (uint32_t, actual payload size in bytes)
 *   offset 20: subsystem_id   (uint32_t, subsystem index: 0=sensor, 1=network, 2=user)
 *   offset 24: payload        (char[512], pipe-separated key=value config data)
 *
 * Total: 536 bytes. Fits well within one 4096-byte page.
 *
 * The 'ready' field serves double duty:
 *   - Value 0 means the buffer is empty / being written
 *   - Value N (>0) means sequence N is ready to read
 *   Writer stores with __ATOMIC_RELEASE after populating all other fields.
 *   Reader loads with __ATOMIC_ACQUIRE before reading other fields.
 */
typedef struct {
    uint32_t ready;                    /* offset  0: release/acquire flag (== sequence when ready) */
    uint32_t sequence;                 /* offset  4: monotonic sequence number */
    int64_t  timestamp_ns;             /* offset  8: CLOCK_REALTIME nanoseconds */
    uint32_t payload_length;           /* offset 16: actual payload size in bytes */
    uint32_t subsystem_id;             /* offset 20: subsystem index (0, 1, or 2) */
    char     payload[SHM_MAX_PAYLOAD]; /* offset 24: pipe-separated key=value data */
} shm_message_t;

/*
 * Subsystem ID mapping — matches the dispatch table in watcher.c / server.c.
 * The Go writer uses these same integer values.
 */
#define SUBSYS_SENSOR_CALIBRATION  0
#define SUBSYS_NETWORK_CONFIG      1
#define SUBSYS_USER_PROFILES       2

/*
 * Compile-time layout verification.
 * If any of these fail, the struct has unexpected padding and the Go
 * writer's byte offsets will be wrong.
 */
_Static_assert(sizeof(shm_message_t) == 536,
               "shm_message_t size mismatch — check padding");
_Static_assert(__builtin_offsetof(shm_message_t, ready) == 0,
               "ready field offset must be 0");
_Static_assert(__builtin_offsetof(shm_message_t, sequence) == 4,
               "sequence field offset must be 4");
_Static_assert(__builtin_offsetof(shm_message_t, timestamp_ns) == 8,
               "timestamp_ns field offset must be 8");
_Static_assert(__builtin_offsetof(shm_message_t, payload_length) == 16,
               "payload_length field offset must be 16");
_Static_assert(__builtin_offsetof(shm_message_t, subsystem_id) == 20,
               "subsystem_id field offset must be 20");
_Static_assert(__builtin_offsetof(shm_message_t, payload) == 24,
               "payload field offset must be 24");

#endif /* SHM_COMMON_H */
