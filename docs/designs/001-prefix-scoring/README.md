# Prefix Hash-Index-Score System

## Summary

The Prefix Hash-Index-Score system enables intelligent request routing to models that have previously processed similar request prefixes, thereby improving Time-To-First-Token (TTFT) by leveraging prefix caching capabilities in modern LLM inference engines. A prefix-aware scorer operates in three phases within the Inference Payload Processor (IPP): a pre-processing plugin computes prefix hashes and queries an indexer to identify models with matching cached prefixes; a scoring plugin assigns higher scores to models with longer prefix matched; and a response extraction plugin updates the indexer with information about which models have processed which prefixes.

## Proposed Architecture

The Prefix Hash-Index-Score system consists of three main components that operate at different stages of the request processing pipeline within the IPP:

1. **PrefixHashing Plugin** (Pre-processing, RequestProcessor)
   - **Type**: `framework.RequestProcessor`
   - **Role**: Executes during the pre-processing phase before model selection
   - **Responsibilities**:
     - Computes prefix hashes for incoming requests by dividing the prompt into fixed-size character blocks
     - Queries the prefix indexer to identify models that have previously processed matching prefixes
     - Stores the computed hashes and model match information in the `CycleState` for use by the scorer
     - Implements a greedy longest-prefix matching strategy to find the best cache matches

2. **RequestPrefixScorer** (Scoring, Scorer)
   - **Type**: `modelselector.Scorer`
   - **Role**: Executes during the model selection scoring phase
   - **Responsibilities**:
     - Retrieves prefix hash state from `CycleState` (stored by PrefixHashing plugin)
     - Scores each candidate model based on its prefix cache match ratio
     - Calculates score as: `matchedBlocks / totalBlocks` (range: 0.0 to 1.0)
     - Models with longer prefix matches receive higher scores, influencing routing decisions

3. **PrefixIndexingExtractor** (Post-processing, Extractor)
   - **Type**: `framework.Extractor`
   - **Role**: Executes during the response extraction phase after request completion, running out of the response hot-path as a data layer event processing component
   - **Responsibilities**:
     - Processes `ResponseEventType` events to track completed requests
     - Updates the prefix indexer with information about which model processed which prefix hashes
     - Maintains the indexer's knowledge of prefix-to-model mappings over time, leveraging LRU and TTL eviction policies.

### Request Body Hashing and Indexing

The hash function implementation uses the xxHash algorithm for fast, high-quality hashing.

1. **Block-based Hashing**: The request prompt is divided into fixed-size text blocks. Each block is hashed independently, creating a sequence of `BlockHash` values.

2. **Chained Hashing**: To ensure that identical prefixes in different positions produce different hashes, each block hash incorporates the previous block's hash:
   ```
   hash(block[i]) = xxhash(block[i].content + hash(block[i-1]))
   ```
   This creates a dependency chain where changing any block affects all subsequent hashes.

3. **Configurable Parameters**:
   - `HashBlockSize`: Size of each text block (default: 64)
   - `MaxPrefixBlocksToMatch`: Maximum number of blocks to hash (default: 1024)

**Hash Function Properties:**
- Fast computation using xxHash (optimized for speed)
- Deterministic: same input always produces same hash
- Collision-resistant: different inputs produce different hashes with high probability
- Order-preserving: prefix relationship is maintained in hash sequences

### Indexing Mechanism

The indexer provides an efficient bidirectional mapping between prefix hashes and models:

1. **Hash-to-Models Mapping** (`hashToModels map[BlockHash]modelSet`):
   - Maps each `BlockHash` to a set of models that have that hash cached
   - Enables fast lookup: "Which models have this prefix hash cached?"

2. **Model-to-LRU Mapping** (`modelToLRU map[ModelID]*lru.Cache[BlockHash, struct{}]`):
   - Each model has its own LRU and TTL cache tracking which hashes it has processed
   - Default capacity: 256,144 entries per model (configurable)
   - Automatically evicts oldest hashes when capacity is reached
   - Approximates the actual prefix cache state on model inference servers

**Eviction and Cleanup**: The indexer uses per-model LRU and TTL caches with custom eviction callbacks that automatically remove models from the hash-to-models mapping when hashes are evicted. A background cleanup goroutine runs every 2 minutes to remove entries for inactive models, preventing memory leaks. All operations are thread-safe using `sync.RWMutex` with read locks for queries and write locks for updates.

**Capacity Sizing**: The initial LRU version uses a default LRU capacity to be revised for in future versions.

### Datastore Updates

The prefix indexer is integrated into the datastore to provide centralized access across all IPP components.

**Interface Extension:**

The `PrefixIndexer` interface is added to avoid import cycles:

```go
type PrefixIndexer interface {
    // Methods defined by implementation
}
```

The `Datastore` interface is extended:

```go
type Datastore interface {
    GetOrCreateModel(name string) datalayer.Model
    DeleteModel(name string)
    Models() []string
    GetPrefixIndexer() PrefixIndexer  // New method
	 SetPrefixIndexer(indexer PrefixIndexer) PrefixIndexer // New method
}
```

**Data Structure:**

The concrete `store` struct is extended:

```go
type store struct {
    mu            sync.RWMutex
    models        map[string]datalayer.Model
    prefixIndexer PrefixIndexer  // New field
}
```

**Initialization:**

The store is created with `prefixIndexer: nil` and initialized lazily when the PrefixHashing plugin is registered. The indexer is set via a `SetPrefixIndexer()` method that uses type assertion to access the setter on the concrete store implementation.

**Access Pattern:**

All three components access the indexer through `datastore.Store.GetPrefixIndexer()`, with nil checks to gracefully handle cases where the indexer is not yet initialized. 

## Open Questions

### 1. Pre-processing Plugin Access to Datastore

**Requirement**: The pre-processing plugin needs access to the datastore instance to query the prefix indexer.

**Approach 1**: The plugin factory uses the global `datastore.Store` variable directly. While this works, it couples the plugin to global state and makes testing more difficult.

**Approach 2**: Extend the `framework.Handle` interface to provide datastore access:
```go
type Handle interface {
    Context() context.Context
    Datastore() datastore.Datastore  // New method
}
```

This would provide explicit dependency injection, easier testing, and eliminate global state coupling. However, it requires framework changes that affect all plugin factories.

### 2. Cycle State for Hash and Model Information

**Question**: Should the computed hashes and model match information be stored in cycle state? If not, how should the pre-processing plugin pass this information to the scorer?

**Suggested Implementation**: The PrefixHashing plugin stores a `RequestHashingState` in the `CycleState` containing:
- `PrefixHashes []BlockHash`: The computed prefix hashes
- `PrefixCacheModels map[ModelID]int`: Models and their longest prefix match length

The RequestPrefixScorer retrieves this state from CycleState to calculate scores.

**Considerations**:
- **Pros**: CycleState is designed for passing data between pipeline stages; thread-safe; scoped to request lifecycle
- **Alternatives**: Direct indexer queries in scorer (slower, redundant computation), shared cache (complex lifecycle management)
