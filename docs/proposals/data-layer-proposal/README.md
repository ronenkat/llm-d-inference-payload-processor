# Proposal: Data Layer

## Summary

Introduce an **Async Data Layer** — a background observation pipeline that runs
**outside the critical request path**. It collects runtime events fired by the producer
(currently [`pkg/handlers/server.go`](../../../pkg/handlers/server.go)), buffers them off the hot path, and dispatches them to registered
`Extractor`s that compute aggregates and write them to the `Datastore` for the Model Selector.

## Goal

Track runtime information about inference requests to help make better routing decisions —
for example, which model has the least in-flight requests or the lowest average latency.
This information is read by the Model Selector (Filter / Score / Pick) when choosing
where to route each request.

## Requirements

- **Non-blocking on the critical path** - data collection must add zero latency to request handling.
- **Multiple independent extractors** - different metrics must be computable independently without coupling to each other.
- **Extensible** - adding a new metric or event type must not require changes to existing extractors or the producer.
- **Off the plugin pipeline** - the data layer is a background concern; it must not participate in the per-request plugin chain.

## Proposal

### Architecture

```mermaid
flowchart TD
    P["Producer\n(pkg/handlers/server.go)"]
    PP["Plugin Pipeline\nFilter → Score → Pick"]
    NS["NotificationSource\nchan Event  (buffered)"]
    EL["event loop"]
    ExtA["RequestMetadataExtractor"]
    ExtB["LatencyExtractor\n(future)"]
    DS[("Datastore\npkg/datastore/inmemory")]
    MS["Model Selector\n(InflightRequestsScorer)"]

    P -->|"Notify(event)  ~ns"| NS
    P --> PP
    NS --> EL
    EL -->|"one event at a time"| ExtA
    EL -->|"one event at a time"| ExtB
    ExtA --> DS
    ExtB --> DS
    DS --> MS
    PP -->|reads| MS
```

The **producer** (currently [`pkg/handlers/server.go`](../../../pkg/handlers/server.go)) fires an `Event` on each request and response —
a non-blocking channel write (~ns). The `NotificationSource` buffers it. A background
event loop reads each event from the channel as it arrives and fans it out to all registered
`Extractor`s. Each extractor switches on `Event.Type` and handles what it understands,
ignoring the rest.

### Types (`pkg/framework/interface/`)

Framework-level interfaces and event types live in [`pkg/framework/interface/datalayer/datasource/types.go`](../../../pkg/framework/interface/datalayer/datasource/types.go).
Model and attribute types live in the [`pkg/framework/interface/datalayer/`](../../../pkg/framework/interface/datalayer/) package.

```go
type DataSource interface {
    Plugin                             // TypedName() TypedName
    Start(ctx context.Context) error
    // Stop signals the component to shut down and blocks until it has fully stopped.
    Stop()
}

// Event is the uniform carrier of all data layer events.
// Type identifies what happened; Payload holds the event-specific data.
type Event struct {
    Type    EventType
    Payload any
}

// EventNotifier is the narrow interface the producer uses to fire events.
// Keeping it separate lets the server depend only on Notify, not on lifecycle
// or extractor registration.
type EventNotifier interface {
    Notify(e Event)
}

// NotificationSource buffers events off the hot path and dispatches
// each event to registered Extractors as it arrives.
type NotificationSource interface {
    DataSource
    EventNotifier
    // RegisterExtractor adds an extractor after construction.
    // Extractors known at construction time can be passed to New directly.
    RegisterExtractor(e Extractor)
}

// Extractor processes a batch of Events. It does not manage its own goroutines.
type Extractor interface {
    Plugin
    Extract(ctx context.Context, events []Event) error
}
```

See [Appendix](#appendix) for payload struct definitions and a full extractor example.

### Datastore injection

The `Datastore` is available to extractor factories via [`plugin.Handle.Datastore()`](../../../pkg/framework/interface/plugin/handle.go).
This keeps construction consistent with other plugins while the `NotificationSource`
remains a pure event dispatcher with no knowledge of storage.

The concrete implementation lives in [`pkg/datastore/inmemory/`](../../../pkg/datastore/inmemory/), separate from the
[`datalayer.Datastore`](../../../pkg/framework/interface/datalayer/datastore.go) interface, to make room for future backends (e.g. Redis):

```go
ds := inmemory.NewDatastore()
extractor := requestmetadata.NewRequestMetadataExtractor(ds)
```

### Registration ([`cmd/runner/runner.go`](../../../cmd/runner/runner.go))

```go
ds := inmemory.NewDatastore()
src, err := notificationsource.New("default", requestmetadata.NewRequestMetadataExtractor(ds))
if err != nil { ... }
if err := src.Start(ctx); err != nil { ... }

```

**Next:** define a configuration story for data layer plugins (NotificationSource, extractors)
consistent with how model-selector plugins are configured via CLI flags.

## Future

- **LatencyExtractor** - handles `ResponseEventType`; per-model avg latency; owns `"pool-latency"` attribute
- **PollingDataSource** - polls inference pool `/metrics` on a ticker; same `Extractor` interface

## Implementation Steps

1. ✅ Add `DataSource`, `EventNotifier`, `Event`, `NotificationSource`, `Extractor`, payload types to [`pkg/framework/interface/datalayer/datasource/types.go`](../../../pkg/framework/interface/datalayer/datasource/types.go)
2. ✅ Add `Model`, `AttributeMap` types to [`pkg/framework/interface/datalayer/`](../../../pkg/framework/interface/datalayer/)
3. ✅ Implement `NotificationSource` (buffered channel + event loop) in [`pkg/framework/plugins/datalayer/notificationsource/`](../../../pkg/framework/plugins/datalayer/notificationsource/)
4. ✅ Implement `RequestMetadataExtractor` in [`pkg/framework/plugins/datalayer/requestmetadata/`](../../../pkg/framework/plugins/datalayer/requestmetadata/)
5. ⏳ Implement `InflightRequestsScorer` in `pkg/framework/plugins/modelselector/scorer/inflightrequests/` (not yet implemented - would read `RequestMetadataAttributeKey` from models)
6. ✅ Wire Datastore + extractor + NotificationSource in [`cmd/runner/runner.go`](../../../cmd/runner/runner.go) (lines 179-196)
7. ⏳ Add `src.Notify(...)` calls to the producer ([`pkg/handlers/server.go`](../../../pkg/handlers/server.go)) alongside existing pipeline dispatch (not yet implemented - would fire `RequestEventType` and `ResponseEventType`)
8. ✅ Config-driven registration of data layer plugins (lines 266-267 in runner.go - factory functions registered)

---

## Appendix

### Payload types

```go
// package datasource (pkg/framework/interface/datalayer/datasource/)

// RequestPayload is the Payload for RequestEventType.
// Carries the already-parsed request — no re-parsing needed.
type RequestPayload struct {
    Request *requesthandling.InferenceRequest
}

// ResponsePayload is the Payload for ResponseEventType.
// Duration is computed by the producer and passed directly.
// All response body fields are accessible via Response.Body.
type ResponsePayload struct {
    Request  *requesthandling.InferenceRequest
    Response *requesthandling.InferenceResponse
    Duration time.Duration
}
```

### Extractor definitions

#### `RequestMetadataExtractor` — owns `"request-metadata"` ([`pkg/framework/plugins/datalayer/requestmetadata`](../../../pkg/framework/plugins/datalayer/requestmetadata/))

| | |
|---|---|
| Handles | `RequestEventType` (increment), `ResponseEventType` (decrement) |
| Reads | `model`, `max_tokens` from `Request.Body` |
| State | `map[string]RequestMetadataCount` — in-flight counters per model |
| Writes | `RequestMetadataCount{Requests int64, Tokens int64}` per model |

```go
type RequestMetadataCount struct {
    Requests int64
    Tokens   int64 // sum of max_tokens across in-flight requests
}
```
