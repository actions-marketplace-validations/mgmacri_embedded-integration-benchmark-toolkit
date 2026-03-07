# Example Sentinel Files

These files demonstrate the inotify sentinel file format used by the benchmark.

## Format

Each sentinel file contains two lines:

1. **Timestamp** — `CLOCK_REALTIME` nanoseconds (Unix epoch) written by the Go sender
2. **Payload** — Pipe-separated `key=value` pairs representing a config update

## How It Works

1. The Go writer creates a temp file (`.tmp_<subsystem>`) and writes the content
2. The Go writer atomically renames it to the subsystem name (e.g., `sensor_calibration`)
3. The atomic rename triggers an `IN_MOVED_TO` inotify event on the C watcher
4. The C watcher reads the file, extracts the timestamp, computes delivery latency
5. The C watcher parses the key=value payload, compares to cached state, applies changes
6. The C watcher deletes the sentinel file

## Files in This Directory

| File | Subsystem | Description |
|---|---|---|
| `sensor_calibration` | Sensor/measurement tuning | 9 config fields |
| `network_config` | Network/connectivity settings | 9 config fields |
| `user_profiles` | User and access management | 7 config fields |

## Adapting for Your Application

Replace the subsystem names and key=value fields with your application's config domains:

```go
// Go writer side — just change the filename and payload
subsystem := "your_subsystem_name"
payload := "param1=value1|param2=value2|param3=value3"
content := fmt.Sprintf("%d\n%s\n", time.Now().UnixNano(), payload)
```

```c
// C watcher side — add one entry to the dispatch table
static subsystem_handler_t handlers[] = {
    { "your_subsystem_name", 0 },
    // ...
    { NULL, 0 }
};
```
