/*
 * subsystem.h — Shared config subsystem dispatch for C benchmark programs
 *
 * Provides the config parsing, caching, and dispatch table used by all C
 * receiver binaries (watcher.c, server.c, shm reader.c). Previously this
 * code was copy-pasted across all three files.
 *
 * Each subsystem has a set of key=value fields. When a new config payload
 * arrives, it is parsed, compared against the cached state, and the cache
 * is updated. The number of changed fields is returned (simulating the
 * "apply config" work that real firmware does).
 *
 * Subsystem names and field schemas are either:
 *   1. Hardcoded defaults (backwards compatible with original benchmark)
 *   2. Loaded from bench_runtime.json at startup (when --config is used)
 *
 * All functions are static inline for single-header inclusion.
 */

#ifndef BENCH_SUBSYSTEM_H
#define BENCH_SUBSYSTEM_H

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>

/* ---------- Config field/state ---------- */

#define SUBSYS_MAX_CONFIG_FIELDS 32
#define SUBSYS_MAX_FIELD_NAME    64
#define SUBSYS_MAX_FIELD_VALUE   64
#define SUBSYS_MAX_SUBSYSTEMS    16
#define SUBSYS_MAX_NAME_LEN      64

typedef struct {
    char name[SUBSYS_MAX_FIELD_NAME];
    char value[SUBSYS_MAX_FIELD_VALUE];
} subsys_field_t;

typedef struct {
    subsys_field_t fields[SUBSYS_MAX_CONFIG_FIELDS];
    int32_t        num_fields;
    int32_t        initialized;
} subsys_state_t;

/* ---------- Subsystem registry ---------- */

typedef struct {
    char           name[SUBSYS_MAX_NAME_LEN];
    subsys_state_t cached;
} subsys_entry_t;

typedef struct {
    subsys_entry_t entries[SUBSYS_MAX_SUBSYSTEMS];
    int32_t        count;
} subsys_registry_t;

/*
 * Initialize a registry with default subsystem names.
 * Call this once at program startup. The hardcoded names match the original
 * benchmark for backwards compatibility.
 */
static inline void subsys_init_defaults(subsys_registry_t *reg) {
    memset(reg, 0, sizeof(subsys_registry_t));
    reg->count = 3;
    snprintf(reg->entries[0].name, SUBSYS_MAX_NAME_LEN, "sensor_calibration");
    snprintf(reg->entries[1].name, SUBSYS_MAX_NAME_LEN, "network_config");
    snprintf(reg->entries[2].name, SUBSYS_MAX_NAME_LEN, "user_profiles");
}

/*
 * Initialize a registry from an array of subsystem names.
 * Used when loading names from bench_runtime.json.
 */
static inline void subsys_init_from_names(subsys_registry_t *reg,
                                           const char **names, int32_t count) {
    memset(reg, 0, sizeof(subsys_registry_t));
    if (count > SUBSYS_MAX_SUBSYSTEMS) count = SUBSYS_MAX_SUBSYSTEMS;
    reg->count = count;
    for (int32_t i = 0; i < count; i++) {
        snprintf(reg->entries[i].name, SUBSYS_MAX_NAME_LEN, "%s", names[i]);
    }
}

/*
 * Look up a subsystem index by name. Returns -1 if not found.
 */
static inline int32_t subsys_get_index(const subsys_registry_t *reg, const char *name) {
    for (int32_t i = 0; i < reg->count; i++) {
        if (strcmp(reg->entries[i].name, name) == 0) {
            return i;
        }
    }
    return -1;
}

/*
 * Get the subsystem name at an index.
 */
static inline const char *subsys_get_name(const subsys_registry_t *reg, int32_t idx) {
    if (idx < 0 || idx >= reg->count) return NULL;
    return reg->entries[idx].name;
}

/*
 * Parse pipe-separated key=value payload, compare to cached state, update cache.
 * Returns number of fields that changed.
 *
 * This is the common implementation previously duplicated in watcher.c, server.c,
 * and reader.c.
 */
static inline int32_t subsys_parse_and_apply(subsys_registry_t *reg,
                                              int32_t subsystem_idx,
                                              const char *payload) {
    if (subsystem_idx < 0 || subsystem_idx >= reg->count) return 0;

    subsys_state_t new_config;
    memset(&new_config, 0, sizeof(new_config));

    const char *p = payload;
    while (*p && new_config.num_fields < SUBSYS_MAX_CONFIG_FIELDS) {
        subsys_field_t *f = &new_config.fields[new_config.num_fields];

        const char *eq = strchr(p, '=');
        if (!eq) break;

        size_t name_len = (size_t)(eq - p);
        if (name_len >= SUBSYS_MAX_FIELD_NAME) name_len = SUBSYS_MAX_FIELD_NAME - 1;
        memcpy(f->name, p, name_len);
        f->name[name_len] = '\0';

        const char *val_start = eq + 1;
        const char *pipe = strchr(val_start, '|');
        size_t val_len;
        if (pipe) {
            val_len = (size_t)(pipe - val_start);
            p = pipe + 1;
        } else {
            val_len = strlen(val_start);
            p = val_start + val_len;
        }
        if (val_len >= SUBSYS_MAX_FIELD_VALUE) val_len = SUBSYS_MAX_FIELD_VALUE - 1;
        memcpy(f->value, val_start, val_len);
        f->value[val_len] = '\0';

        new_config.num_fields++;
    }

    subsys_state_t *cached = &reg->entries[subsystem_idx].cached;
    int32_t changes = 0;

    if (!cached->initialized) {
        changes = new_config.num_fields;
    } else {
        for (int32_t i = 0; i < new_config.num_fields; i++) {
            int found = 0;
            for (int32_t j = 0; j < cached->num_fields; j++) {
                if (strcmp(new_config.fields[i].name, cached->fields[j].name) == 0) {
                    found = 1;
                    if (strcmp(new_config.fields[i].value, cached->fields[j].value) != 0) {
                        changes++;
                    }
                    break;
                }
            }
            if (!found) changes++;
        }
    }

    memcpy(cached, &new_config, sizeof(subsys_state_t));
    cached->initialized = 1;

    return changes;
}

#endif /* BENCH_SUBSYSTEM_H */
