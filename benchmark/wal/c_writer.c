/*
 * c_writer.c — C data writer for SQLite WAL contention benchmark
 *
 * Simulates a high-frequency data logging process (the typical "firmware" or
 * "embedded application" side). Inserts rows into sample_data using the
 * prepare/bind/step/finalize pattern — the standard approach for embedded
 * SQLite applications.
 *
 * CLI: ./c_writer <db_path> <interval_ms> <duration_sec>
 * Env: BUSY_TIMEOUT (default 5000, set to 0 for no-retry baseline)
 *
 * Output: JSON to stdout, progress to stderr
 *
 * Customize for your project:
 *   - Change the INSERT to match your schema
 *   - Adjust data generation to match your payload size
 *   - Change interval_ms to match your write frequency
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <signal.h>
#include <time.h>
#include <unistd.h>
#include <sqlite3.h>

/* Shared headers — schema-agnostic binding support */
#include "../common/bench_runtime.h"
#include "../common/latency.h"

/* ---------- Signal handling ---------- */

static volatile sig_atomic_t g_stop = 0;

static void sigint_handler(int sig) {
    (void)sig;
    g_stop = 1;
}

/* ---------- Helpers ---------- */

static void get_current_datetime(char *date_buf, size_t dlen, char *time_buf, size_t tlen) {
    time_t now = time(NULL);
    struct tm *tm = localtime(&now);
    strftime(date_buf, dlen, "%Y-%m-%d", tm);
    strftime(time_buf, tlen, "%H:%M:%S", tm);
}

/* ---------- Schema-agnostic binding ---------- */

/*
 * bind_column_dynamic() binds a single column based on its affinity and hint
 * from the runtime config. This replaces the 19 hardcoded sqlite3_bind_*
 * calls with data-driven binding for any schema.
 *
 * Affinity mapping:
 *   "integer" → sqlite3_bind_int with hint-appropriate range
 *   "text"    → sqlite3_bind_text with hint-appropriate content
 *   "real"    → sqlite3_bind_double with reasonable range
 *   other     → sqlite3_bind_null
 */
static void bind_column_dynamic(sqlite3_stmt *stmt, int col_idx,
                                const bench_column_t *col, int record_id) {
    int bind_pos = col_idx + 1;  /* SQLite bind positions are 1-based */

    if (strcmp(col->affinity, "integer") == 0) {
        if (strcmp(col->hint, "id") == 0) {
            sqlite3_bind_int(stmt, bind_pos, record_id);
        } else {
            sqlite3_bind_int(stmt, bind_pos, rand() % 1000);
        }
    } else if (strcmp(col->affinity, "text") == 0) {
        if (strcmp(col->hint, "date") == 0) {
            char buf[16];
            time_t now = time(NULL);
            struct tm *tm = localtime(&now);
            strftime(buf, sizeof(buf), "%Y-%m-%d", tm);
            sqlite3_bind_text(stmt, bind_pos, buf, -1, SQLITE_TRANSIENT);
        } else if (strcmp(col->hint, "time") == 0) {
            char buf[16];
            time_t now = time(NULL);
            struct tm *tm = localtime(&now);
            strftime(buf, sizeof(buf), "%H:%M:%S", tm);
            sqlite3_bind_text(stmt, bind_pos, buf, -1, SQLITE_TRANSIENT);
        } else if (strcmp(col->hint, "flag") == 0) {
            sqlite3_bind_text(stmt, bind_pos, (rand() % 100 < 80) ? "P" : "F",
                              -1, SQLITE_STATIC);
        } else if (strcmp(col->hint, "serial") == 0) {
            char buf[32];
            snprintf(buf, sizeof(buf), "BENCH-%03d", record_id % 1000);
            sqlite3_bind_text(stmt, bind_pos, buf, -1, SQLITE_TRANSIENT);
        } else if (strcmp(col->hint, "name") == 0) {
            sqlite3_bind_text(stmt, bind_pos, "BenchOp", -1, SQLITE_STATIC);
        } else if (strcmp(col->hint, "label") == 0) {
            sqlite3_bind_text(stmt, bind_pos, "S", -1, SQLITE_STATIC);
        } else {
            /* Generic text: use column name as prefix */
            char buf[64];
            snprintf(buf, sizeof(buf), "%s_%d", col->name, record_id);
            sqlite3_bind_text(stmt, bind_pos, buf, -1, SQLITE_TRANSIENT);
        }
    } else if (strcmp(col->affinity, "real") == 0) {
        double val = (double)(rand() % 10000) / 100.0 - 50.0;
        sqlite3_bind_double(stmt, bind_pos, val);
    } else {
        sqlite3_bind_null(stmt, bind_pos);
    }
}

/* ---------- JSON output ---------- */

static void print_json(FILE *out,
                       const char *journal_mode,
                       int busy_timeout_ms,
                       int interval_ms,
                       int duration_sec,
                       int total_writes,
                       int successful_writes,
                       int sqlite_busy_count,
                       int sqlite_error_count,
                       latency_array_t *la)
{
    latency_sort(la);
    latency_stats_t st = latency_compute_stats(la);

    fprintf(out, "{\n");
    fprintf(out, "  \"role\": \"c_writer\",\n");
    fprintf(out, "  \"journal_mode\": \"%s\",\n", journal_mode);
    fprintf(out, "  \"busy_timeout_ms\": %d,\n", busy_timeout_ms);
    fprintf(out, "  \"interval_ms\": %d,\n", interval_ms);
    fprintf(out, "  \"duration_sec\": %d,\n", duration_sec);
    fprintf(out, "  \"total_writes\": %d,\n", total_writes);
    fprintf(out, "  \"successful_writes\": %d,\n", successful_writes);
    fprintf(out, "  \"sqlite_busy_count\": %d,\n", sqlite_busy_count);
    fprintf(out, "  \"sqlite_error_count\": %d,\n", sqlite_error_count);
    fprintf(out, "  \"write_latency_us\": {\n");
    fprintf(out, "    \"min\": %ld,\n", (long)st.min);
    fprintf(out, "    \"p50\": %ld,\n", (long)st.p50);
    fprintf(out, "    \"p95\": %ld,\n", (long)st.p95);
    fprintf(out, "    \"p99\": %ld,\n", (long)st.p99);
    fprintf(out, "    \"max\": %ld\n",  (long)st.max);
    fprintf(out, "  }\n");
    fprintf(out, "}\n");
}

/* ---------- Main ---------- */

int main(int argc, char *argv[]) {
    if (argc != 4) {
        fprintf(stderr, "Usage: %s <db_path> <interval_ms> <duration_sec>\n", argv[0]);
        return 1;
    }

    const char *db_path    = argv[1];
    int interval_ms        = atoi(argv[2]);
    int duration_sec       = atoi(argv[3]);

    if (interval_ms <= 0 || duration_sec <= 0) {
        fprintf(stderr, "[c_writer] Invalid interval_ms or duration_sec\n");
        return 1;
    }

    /* Signal setup */
    struct sigaction sa = { .sa_handler = sigint_handler };
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = 0;
    sigaction(SIGINT, &sa, NULL);
    sigaction(SIGTERM, &sa, NULL);

    /* Busy timeout from env (default 5000ms) */
    int busy_timeout_ms = 5000;
    const char *bt_env = getenv("BUSY_TIMEOUT");
    if (bt_env) {
        busy_timeout_ms = atoi(bt_env);
    }

    /* Open database — bare sqlite3_open, inheriting journal mode from orchestrator */
    sqlite3 *db = NULL;
    int rc = sqlite3_open(db_path, &db);
    if (rc != SQLITE_OK) {
        fprintf(stderr, "[c_writer] Cannot open database: %s\n", sqlite3_errmsg(db));
        return 1;
    }

    if (busy_timeout_ms > 0) {
        sqlite3_busy_timeout(db, busy_timeout_ms);
    }

    /* Detect journal mode for reporting */
    char journal_mode[16] = "unknown";
    {
        sqlite3_stmt *pragma_stmt = NULL;
        if (sqlite3_prepare_v2(db, "PRAGMA journal_mode;", -1, &pragma_stmt, NULL) == SQLITE_OK) {
            if (sqlite3_step(pragma_stmt) == SQLITE_ROW) {
                const char *mode = (const char *)sqlite3_column_text(pragma_stmt, 0);
                if (mode) {
                    snprintf(journal_mode, sizeof(journal_mode), "%s", mode);
                }
            }
            sqlite3_finalize(pragma_stmt);
        }
    }

    fprintf(stderr, "[c_writer] db=%s interval=%dms duration=%ds busy_timeout=%d journal=%s\n",
            db_path, interval_ms, duration_sec, busy_timeout_ms, journal_mode);

    /*
     * Schema-agnostic SQL: try runtime config first, fall back to hardcoded.
     *
     * When the bench CLI orchestrator is used, /tmp/bench_runtime.json
     * contains INSERT SQL and column metadata derived from schema introspection.
     * When running standalone, the original 19-column INSERT is used.
     */
    bench_runtime_t rt;
    int use_runtime = (bench_runtime_load(&rt, BENCH_RUNTIME_PATH) == 0 &&
                       rt.loaded && rt.schema.insert_sql[0] != '\0');

    const char *sql;
    const char *hardcoded_sql =
        "INSERT INTO sample_data "
        "(record_id, date, time, target_value_1, target_value_2, "
        "result_flag, actual_value_1, actual_value_2, "
        "final_value_1, unit_type, duration_ms, operator_name, "
        "device_serial, coord_x, coord_y, "
        "source_label, category_id, final_value_2, reserved) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);";

    if (use_runtime) {
        sql = rt.schema.insert_sql;
        fprintf(stderr, "[c_writer] Using runtime config: table=%s cols=%d\n",
                rt.schema.table_name, rt.schema.column_count);
    } else {
        sql = hardcoded_sql;
        fprintf(stderr, "[c_writer] Using hardcoded 19-column INSERT (no runtime config)\n");
    }

    sqlite3_stmt *stmt = NULL;
    rc = sqlite3_prepare_v2(db, sql, -1, &stmt, NULL);
    if (rc != SQLITE_OK) {
        fprintf(stderr, "[c_writer] Prepare failed: %s\n", sqlite3_errmsg(db));
        sqlite3_close(db);
        return 1;
    }

    /* Init state */
    srand(42);
    latency_array_t latencies;
    latency_init(&latencies);

    int total_writes      = 0;
    int successful_writes = 0;
    int sqlite_busy_count = 0;
    int sqlite_error_count = 0;
    int record_id         = 0;

    struct timespec start_time;
    clock_gettime(CLOCK_MONOTONIC, &start_time);

    while (!g_stop) {
        struct timespec now;
        clock_gettime(CLOCK_MONOTONIC, &now);
        double elapsed = (now.tv_sec - start_time.tv_sec) +
                         (now.tv_nsec - start_time.tv_nsec) / 1e9;
        if (elapsed >= (double)duration_sec) break;

        record_id++;

        /* Generate and bind data — runtime config or hardcoded */
        sqlite3_reset(stmt);
        sqlite3_clear_bindings(stmt);

        if (use_runtime) {
            /* Schema-agnostic: bind based on column metadata */
            for (int32_t c = 0; c < rt.schema.column_count; c++) {
                bind_column_dynamic(stmt, c, &rt.schema.columns[c], record_id);
            }
        } else {
            /* Original hardcoded 19-column bind for backwards compatibility */
            char date_buf[16], time_buf[16];
            get_current_datetime(date_buf, sizeof(date_buf), time_buf, sizeof(time_buf));

            int target_val_1 = 50 + rand() % 451;
            int target_val_2 = rand() % 361;
            const char *result_flag = (rand() % 100 < 80) ? "P" : "F";
            int variance    = target_val_1 / 10;
            int actual_val_1 = target_val_1 - variance + rand() % (2 * variance + 1);
            int actual_val_2 = target_val_2 - 5 + rand() % 11;
            int final_val_1  = actual_val_1;
            int final_val_2  = actual_val_2;
            int unit_type    = rand() % 3;
            int duration_val = 100 + rand() % 4901;
            int category_id  = (record_id % 5) + 1;

            sqlite3_bind_int(stmt,  1,  record_id);
            sqlite3_bind_text(stmt, 2,  date_buf, -1, SQLITE_TRANSIENT);
            sqlite3_bind_text(stmt, 3,  time_buf, -1, SQLITE_TRANSIENT);
            sqlite3_bind_int(stmt,  4,  target_val_1);
            sqlite3_bind_int(stmt,  5,  target_val_2);
            sqlite3_bind_text(stmt, 6,  result_flag, -1, SQLITE_STATIC);
            sqlite3_bind_int(stmt,  7,  actual_val_1);
            sqlite3_bind_int(stmt,  8,  actual_val_2);
            sqlite3_bind_int(stmt,  9,  final_val_1);
            sqlite3_bind_int(stmt,  10, unit_type);
            sqlite3_bind_int(stmt,  11, duration_val);
            sqlite3_bind_text(stmt, 12, "BenchOp", -1, SQLITE_STATIC);
            sqlite3_bind_text(stmt, 13, "BENCH-001", -1, SQLITE_STATIC);
            sqlite3_bind_int(stmt,  14, -793000);
            sqlite3_bind_int(stmt,  15,  436000);
            sqlite3_bind_text(stmt, 16, "S", -1, SQLITE_STATIC);
            sqlite3_bind_int(stmt,  17, category_id);
            sqlite3_bind_int(stmt,  18, final_val_2);
            sqlite3_bind_int(stmt,  19, 0);
        }

        /* Time only the sqlite3_step() call */
        struct timespec t0, t1;
        clock_gettime(CLOCK_MONOTONIC, &t0);
        rc = sqlite3_step(stmt);
        clock_gettime(CLOCK_MONOTONIC, &t1);

        int64_t elapsed_us = (t1.tv_sec - t0.tv_sec) * 1000000LL +
                             (t1.tv_nsec - t0.tv_nsec) / 1000LL;

        total_writes++;

        if (rc == SQLITE_DONE) {
            successful_writes++;
            latency_push(&latencies, elapsed_us);
        } else if (rc == SQLITE_BUSY) {
            sqlite_busy_count++;
            fprintf(stderr, "[c_writer] SQLITE_BUSY on write #%d\n", total_writes);
        } else {
            sqlite_error_count++;
            fprintf(stderr, "[c_writer] Error on write #%d: %s (rc=%d)\n",
                    total_writes, sqlite3_errmsg(db), rc);
        }

        if (total_writes % 100 == 0) {
            fprintf(stderr, "[c_writer] %d writes completed\n", total_writes);
        }

        usleep((useconds_t)(interval_ms * 1000));
    }

    sqlite3_finalize(stmt);
    sqlite3_close(db);

    print_json(stdout, journal_mode, busy_timeout_ms, interval_ms, duration_sec,
               total_writes, successful_writes, sqlite_busy_count, sqlite_error_count,
               &latencies);

    latency_free(&latencies);

    fprintf(stderr, "[c_writer] Done: %d/%d writes, %d busy, %d errors\n",
            successful_writes, total_writes, sqlite_busy_count, sqlite_error_count);

    return 0;
}
