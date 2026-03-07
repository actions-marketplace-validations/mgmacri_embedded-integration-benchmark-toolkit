/*
 * bench_runtime.h — Runtime configuration reader for C benchmark binaries
 *
 * The Go orchestrator resolves bench.yaml into a simple JSON file at a known
 * path (/tmp/bench_runtime.json). C binaries read this file at startup to
 * get schema-derived SQL, column metadata, and subsystem definitions without
 * needing a YAML parser.
 *
 * JSON bridge format (written by Go orchestrator):
 * {
 *   "schema": {
 *     "table": "sample_data",
 *     "insert_sql": "INSERT INTO sample_data (...) VALUES (?, ?, ...);",
 *     "column_count": 19,
 *     "columns": [
 *       {"name": "record_id", "affinity": "integer", "hint": "id"},
 *       {"name": "date", "affinity": "text", "hint": "date"},
 *       ...
 *     ]
 *   },
 *   "subsystems": [
 *     {
 *       "name": "sensor_calibration",
 *       "field_count": 20,
 *       "fields": [
 *         {"name": "temp_offset", "type": "int", "min": -50, "max": 50},
 *         {"name": "sensor_id", "type": "text", "values": ["SNS-001","SNS-002"]}
 *       ]
 *     }
 *   ],
 *   "paths": {
 *     "watch_dir": "/tmp/sentinel_bench",
 *     "socket_path": "/tmp/bench_ipc.sock",
 *     "shm_name": "/bench_shm",
 *     "fifo_path": "/tmp/bench_shm_fifo"
 *   }
 * }
 *
 * Parsing approach:
 *   This header provides a minimal hand-rolled JSON parser sufficient for
 *   the runtime config format. It does NOT depend on cJSON or any external
 *   library, keeping the cross-compilation simple. It only handles the
 *   specific structure above — it is NOT a general-purpose JSON parser.
 *
 * Backwards compatibility:
 *   When no --config flag is given, binaries fall back to their original
 *   hardcoded behavior. The runtime config is purely additive.
 *
 * ARM portability:
 *   Uses only fixed-width types and standard C99 string functions.
 *   No dynamic-linking dependencies beyond libc.
 */

#ifndef BENCH_RUNTIME_H
#define BENCH_RUNTIME_H

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

/* ---------- Constants ---------- */

#define BENCH_RUNTIME_PATH     "/tmp/bench_runtime.json"
#define BENCH_MAX_COLUMNS      64
#define BENCH_MAX_SUBSYSTEMS   16
#define BENCH_MAX_FIELDS       32
#define BENCH_MAX_NAME_LEN     64
#define BENCH_MAX_SQL_LEN      2048
#define BENCH_MAX_PATH_LEN     256
#define BENCH_MAX_VALUES       16
#define BENCH_MAX_VALUE_LEN    64

/* ---------- Column metadata ---------- */

typedef struct {
    char     name[BENCH_MAX_NAME_LEN];
    char     affinity[16];   /* "integer", "text", "real", "blob" */
    char     hint[16];       /* "id", "date", "time", "flag", "name", "serial", "label", "" */
} bench_column_t;

/* ---------- Schema metadata ---------- */

typedef struct {
    char           table_name[BENCH_MAX_NAME_LEN];
    char           insert_sql[BENCH_MAX_SQL_LEN];
    char           select_sql[BENCH_MAX_SQL_LEN];
    int32_t        column_count;
    bench_column_t columns[BENCH_MAX_COLUMNS];
} bench_schema_t;

/* ---------- Subsystem field metadata ---------- */

typedef struct {
    char    name[BENCH_MAX_NAME_LEN];
    char    type[8];         /* "int" or "text" */
    int32_t min_val;         /* for type=int */
    int32_t max_val;         /* for type=int */
    int32_t value_count;     /* number of text values */
    char    values[BENCH_MAX_VALUES][BENCH_MAX_VALUE_LEN];
} bench_field_t;

typedef struct {
    char          name[BENCH_MAX_NAME_LEN];
    int32_t       field_count;
    bench_field_t fields[BENCH_MAX_FIELDS];
} bench_subsystem_t;

/* ---------- Paths ---------- */

typedef struct {
    char watch_dir[BENCH_MAX_PATH_LEN];
    char socket_path[BENCH_MAX_PATH_LEN];
    char shm_name[BENCH_MAX_PATH_LEN];
    char fifo_path[BENCH_MAX_PATH_LEN];
} bench_paths_t;

/* ---------- Top-level runtime config ---------- */

typedef struct {
    bench_schema_t     schema;
    int32_t            subsystem_count;
    bench_subsystem_t  subsystems[BENCH_MAX_SUBSYSTEMS];
    bench_paths_t      paths;
    int32_t            loaded;   /* 1 if successfully loaded, 0 otherwise */
} bench_runtime_t;

/* ---------- Minimal JSON string extraction ---------- */

/*
 * Find a JSON string value for a given key in a JSON object.
 * Searches for "key": "value" and copies value into dst.
 * Returns 0 on success, -1 if not found.
 */
static int bench_json_get_string(const char *json, const char *key, char *dst, size_t dst_len) {
    char pattern[BENCH_MAX_NAME_LEN + 8];
    snprintf(pattern, sizeof(pattern), "\"%s\"", key);

    const char *pos = strstr(json, pattern);
    if (!pos) return -1;

    /* Skip past key and find the colon */
    pos += strlen(pattern);
    while (*pos && (*pos == ' ' || *pos == ':' || *pos == '\t' || *pos == '\n' || *pos == '\r')) pos++;

    if (*pos != '"') return -1;
    pos++; /* skip opening quote */

    size_t i = 0;
    while (*pos && *pos != '"' && i < dst_len - 1) {
        if (*pos == '\\' && *(pos + 1)) {
            pos++;  /* skip escape */
        }
        dst[i++] = *pos++;
    }
    dst[i] = '\0';
    return 0;
}

/*
 * Find a JSON integer value for a given key.
 * Returns the integer value, or default_val if not found.
 */
static int32_t bench_json_get_int(const char *json, const char *key, int32_t default_val) {
    char pattern[BENCH_MAX_NAME_LEN + 8];
    snprintf(pattern, sizeof(pattern), "\"%s\"", key);

    const char *pos = strstr(json, pattern);
    if (!pos) return default_val;

    pos += strlen(pattern);
    while (*pos && (*pos == ' ' || *pos == ':' || *pos == '\t' || *pos == '\n' || *pos == '\r')) pos++;

    return (int32_t)strtol(pos, NULL, 10);
}

/*
 * Find a JSON array of strings for a given key.
 * Populates dst array and returns count.
 */
__attribute__((unused))
static int32_t bench_json_get_string_array(const char *json, const char *key,
                                            char dst[][BENCH_MAX_VALUE_LEN],
                                            int32_t max_count) {
    char pattern[BENCH_MAX_NAME_LEN + 8];
    snprintf(pattern, sizeof(pattern), "\"%s\"", key);

    const char *pos = strstr(json, pattern);
    if (!pos) return 0;

    pos += strlen(pattern);
    while (*pos && *pos != '[') pos++;
    if (*pos != '[') return 0;
    pos++; /* skip [ */

    int32_t count = 0;
    while (*pos && *pos != ']' && count < max_count) {
        while (*pos && (*pos == ' ' || *pos == ',' || *pos == '\t' || *pos == '\n' || *pos == '\r')) pos++;
        if (*pos == '"') {
            pos++;
            size_t i = 0;
            while (*pos && *pos != '"' && i < BENCH_MAX_VALUE_LEN - 1) {
                if (*pos == '\\' && *(pos + 1)) pos++;
                dst[count][i++] = *pos++;
            }
            dst[count][i] = '\0';
            if (*pos == '"') pos++;
            count++;
        } else {
            break;
        }
    }
    return count;
}

/*
 * Load runtime config from a file path (usually BENCH_RUNTIME_PATH).
 * Returns 0 on success, -1 on failure. On failure, rt->loaded remains 0
 * and the caller should fall back to hardcoded defaults.
 */
static int bench_runtime_load(bench_runtime_t *rt, const char *path) {
    memset(rt, 0, sizeof(bench_runtime_t));

    if (!path) path = BENCH_RUNTIME_PATH;

    FILE *f = fopen(path, "r");
    if (!f) {
        fprintf(stderr, "[bench_runtime] No config at %s — using defaults\n", path);
        return -1;
    }

    /* Read entire file */
    fseek(f, 0, SEEK_END);
    long fsize = ftell(f);
    fseek(f, 0, SEEK_SET);

    if (fsize <= 0 || fsize > 1024 * 1024) {
        fprintf(stderr, "[bench_runtime] Config file too large or empty\n");
        fclose(f);
        return -1;
    }

    char *json = malloc((size_t)fsize + 1);
    if (!json) {
        fclose(f);
        return -1;
    }
    size_t nread = fread(json, 1, (size_t)fsize, f);
    fclose(f);
    json[nread] = '\0';

    /* Parse schema section */
    bench_json_get_string(json, "table", rt->schema.table_name, sizeof(rt->schema.table_name));
    bench_json_get_string(json, "insert_sql", rt->schema.insert_sql, sizeof(rt->schema.insert_sql));
    bench_json_get_string(json, "select_sql", rt->schema.select_sql, sizeof(rt->schema.select_sql));
    rt->schema.column_count = bench_json_get_int(json, "column_count", 0);

    /* Parse paths */
    bench_json_get_string(json, "watch_dir", rt->paths.watch_dir, sizeof(rt->paths.watch_dir));
    bench_json_get_string(json, "socket_path", rt->paths.socket_path, sizeof(rt->paths.socket_path));
    bench_json_get_string(json, "shm_name", rt->paths.shm_name, sizeof(rt->paths.shm_name));
    bench_json_get_string(json, "fifo_path", rt->paths.fifo_path, sizeof(rt->paths.fifo_path));

    /* Note: Column and subsystem array parsing would require iterating JSON
     * arrays of objects. For the MVP, the C binaries receive the INSERT SQL
     * directly and use it as-is. Full column/subsystem metadata parsing is
     * a Phase 2 enhancement when C binaries need to generate data
     * dynamically instead of receiving pre-generated SQL from Go. */

    /* Parse columns array — find "columns": [...] and extract each object */
    {
        const char *cols_key = strstr(json, "\"columns\"");
        if (cols_key) {
            const char *arr_start = strchr(cols_key, '[');
            if (arr_start) {
                const char *p = arr_start + 1;
                int32_t col_idx = 0;
                while (*p && *p != ']' && col_idx < BENCH_MAX_COLUMNS) {
                    /* Find next object start */
                    while (*p && *p != '{') {
                        if (*p == ']') goto cols_done;
                        p++;
                    }
                    if (*p != '{') break;

                    /* Find object end */
                    const char *obj_start = p;
                    int depth = 1;
                    p++;
                    while (*p && depth > 0) {
                        if (*p == '{') depth++;
                        if (*p == '}') depth--;
                        p++;
                    }

                    /* Extract fields from this object substring */
                    size_t obj_len = (size_t)(p - obj_start);
                    char obj_buf[512];
                    if (obj_len < sizeof(obj_buf)) {
                        memcpy(obj_buf, obj_start, obj_len);
                        obj_buf[obj_len] = '\0';

                        bench_json_get_string(obj_buf, "name",
                            rt->schema.columns[col_idx].name,
                            sizeof(rt->schema.columns[col_idx].name));
                        bench_json_get_string(obj_buf, "affinity",
                            rt->schema.columns[col_idx].affinity,
                            sizeof(rt->schema.columns[col_idx].affinity));
                        bench_json_get_string(obj_buf, "hint",
                            rt->schema.columns[col_idx].hint,
                            sizeof(rt->schema.columns[col_idx].hint));

                        col_idx++;
                    }
                }
cols_done:
                /* Update column count if we successfully parsed columns */
                if (col_idx > 0 && rt->schema.column_count == 0) {
                    rt->schema.column_count = col_idx;
                }
            }
        }
    }

    /* Parse subsystems array — find "subsystems": [...] and extract names */
    {
        const char *subs_key = strstr(json, "\"subsystems\"");
        if (subs_key) {
            const char *arr_start = strchr(subs_key, '[');
            if (arr_start) {
                const char *p = arr_start + 1;
                int32_t sub_idx = 0;
                while (*p && *p != ']' && sub_idx < BENCH_MAX_SUBSYSTEMS) {
                    /* Find next object start */
                    while (*p && *p != '{') {
                        if (*p == ']') goto subs_done;
                        p++;
                    }
                    if (*p != '{') break;

                    /* Find object end */
                    const char *obj_start = p;
                    int depth = 1;
                    p++;
                    while (*p && depth > 0) {
                        if (*p == '{') depth++;
                        if (*p == '}') depth--;
                        p++;
                    }

                    /* Extract name from this subsystem object */
                    size_t obj_len = (size_t)(p - obj_start);
                    char obj_buf[512];
                    if (obj_len < sizeof(obj_buf)) {
                        memcpy(obj_buf, obj_start, obj_len);
                        obj_buf[obj_len] = '\0';

                        bench_json_get_string(obj_buf, "name",
                            rt->subsystems[sub_idx].name,
                            sizeof(rt->subsystems[sub_idx].name));

                        if (rt->subsystems[sub_idx].name[0] != '\0') {
                            sub_idx++;
                        }
                    }
                }
subs_done:
                rt->subsystem_count = sub_idx;
            }
        }
    }

    rt->loaded = 1;
    free(json);

    fprintf(stderr, "[bench_runtime] Loaded config: table=%s cols=%d subsystems=%d\n",
            rt->schema.table_name, rt->schema.column_count, rt->subsystem_count);

    return 0;
}

#endif /* BENCH_RUNTIME_H */
