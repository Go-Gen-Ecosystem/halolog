// Copyright 2025 Admilson B. F. Cossa
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package types provides core type definitions
// Author: Admilson B. F. Cossa

package types

import (
	"math/bits"
	"strconv"
	"sync"
	"sync/atomic"
)

// IndexedFieldStore provides unlimited field storage with chunk-based O(1) access
type IndexedFieldStore struct {
	keys         [][]string      // Dynamic chunks of keys
	values       [][]string      // Dynamic chunks of values
	bitmasks     []uint64        // Multiple bitmasks for unlimited fields
	chunkSize    int             // Size of each chunk (default 64)
	activeChunks atomic.Int32    // Number of active chunks
	mu           sync.RWMutex    // Still fine-grained, but per-chunk
	dictionary   FieldDictionary // Field dictionary for auto-registration

	// Lock-free fast path for common operations
	fastPath atomic.Bool // Indicates if fast path is available

	// Fallback field-ID registry used when no dictionary is configured. It
	// assigns small, dense, incremental IDs so the chunked backing store never
	// has to grow unboundedly (a hash-based ID could be ~4 billion, which spun
	// the chunk-growth loop forever).
	fallbackMu  sync.Mutex
	fallbackIDs map[string]int
}

// BasicIndexedFieldStore is the legacy field store kept for backward compatibility.
type BasicIndexedFieldStore struct {
	keys    []string       // Pre-allocated key array
	values  []string       // Pre-allocated value array
	bitmask uint64         // Active field bitmask (up to 64 fields)
	mu      sync.RWMutex   // Fine-grained locking
	keyMap  map[string]int // O(1) key to index mapping
}

// NewIndexedFieldStore creates a new Indexed field store with unlimited capacity.
// Note: dictionary should be set separately using dependency injection.
func NewIndexedFieldStore(chunkSize int) *IndexedFieldStore {
	if chunkSize <= 0 {
		chunkSize = 64 // Default chunk size
	}

	return &IndexedFieldStore{
		chunkSize:  chunkSize,
		dictionary: nil, // Will be set via dependency injection
	}
}

// NewIndexedFieldStoreWithDict creates a new Indexed field store with a custom dictionary.
func NewIndexedFieldStoreWithDict(chunkSize int, dictionary FieldDictionary) *IndexedFieldStore {
	if chunkSize <= 0 {
		chunkSize = 64 // Default chunk size
	}
	// Note: dictionary can be nil, will be set by caller if needed

	return &IndexedFieldStore{
		chunkSize:  chunkSize,
		dictionary: dictionary,
	}
}

// NewBasicIndexedFieldStore creates a new Indexed field store with pre-allocated capacity
func NewBasicIndexedFieldStore(capacity int) *BasicIndexedFieldStore {
	if capacity > 64 {
		capacity = 64 // Limit to 64 fields for bitmask optimization
	}
	return &BasicIndexedFieldStore{
		keys:    make([]string, 0, capacity),
		values:  make([]string, 0, capacity),
		bitmask: 0,
		keyMap:  make(map[string]int, capacity),
	}
}

// Set adds or updates a field with High-performance O(1) performance
func (qfs *BasicIndexedFieldStore) Set(key, value string) {
	qfs.mu.Lock()
	defer qfs.mu.Unlock()

	// O(1) lookup using hash map
	if idx, exists := qfs.keyMap[key]; exists {
		qfs.values[idx] = value
		qfs.bitmask |= (1 << uint(idx)) // Set bit
		return
	}

	// Add new field if capacity allows
	if len(qfs.keys) < cap(qfs.keys) {
		idx := len(qfs.keys)
		qfs.keys = append(qfs.keys, key)
		qfs.values = append(qfs.values, value)
		qfs.keyMap[key] = idx
		qfs.bitmask |= (1 << uint(idx)) // Set bit
	}
}

// Get retrieves a field value with O(1) performance
func (qfs *BasicIndexedFieldStore) Get(key string) (string, bool) {
	qfs.mu.RLock()
	defer qfs.mu.RUnlock()

	// O(1) lookup using hash map
	if idx, exists := qfs.keyMap[key]; exists && (qfs.bitmask&(1<<uint(idx))) != 0 {
		return qfs.values[idx], true
	}
	return "", false
}

// Iterate processes only active fields using bit-mask traversal - high-performance
// Indexed-inspired: evaluate all possibilities in parallel via bitmask
func (qfs *BasicIndexedFieldStore) Iterate(fn func(key, value string)) {
	qfs.mu.RLock()
	defer qfs.mu.RUnlock()

	// Traverse only set bits - O(popcount(bits)) instead of O(n)
	for activeBits := qfs.bitmask; activeBits != 0; {
		// Find lowest set bit (trailing zeros)
		idx := bits.TrailingZeros64(activeBits)
		activeBits &= activeBits - 1 // Clear lowest set bit

		if idx < len(qfs.keys) && idx < len(qfs.values) {
			fn(qfs.keys[idx], qfs.values[idx])
		}
	}
}

// Reset clears the Indexed field store for reuse
func (qfs *BasicIndexedFieldStore) Reset() {
	qfs.mu.Lock()
	defer qfs.mu.Unlock()

	qfs.bitmask = 0
	qfs.keys = qfs.keys[:0]
	qfs.values = qfs.values[:0]
	// Clear the hash map
	for k := range qfs.keyMap {
		delete(qfs.keyMap, k)
	}
}

// Size returns the number of active fields
func (qfs *BasicIndexedFieldStore) Size() int {
	qfs.mu.RLock()
	defer qfs.mu.RUnlock()
	return bits.OnesCount64(qfs.bitmask)
}

// GetActiveBits returns the raw bitmask for operations
func (qfs *BasicIndexedFieldStore) GetActiveBits() uint64 {
	qfs.mu.RLock()
	defer qfs.mu.RUnlock()
	return qfs.bitmask
}

// SetInt adds an integer field with High-performance conversion
func (qfs *BasicIndexedFieldStore) SetInt(key string, value int64) {
	qfs.mu.Lock()
	defer qfs.mu.Unlock()

	// O(1) lookup using hash map
	if idx, exists := qfs.keyMap[key]; exists {
		qfs.values[idx] = strconv.FormatInt(value, 10)
		qfs.bitmask |= (1 << uint(idx)) // Set bit
		return
	}

	// Add new field if capacity allows
	if len(qfs.keys) < cap(qfs.keys) {
		idx := len(qfs.keys)
		qfs.keys = append(qfs.keys, key)
		qfs.values = append(qfs.values, strconv.FormatInt(value, 10))
		qfs.keyMap[key] = idx
		qfs.bitmask |= (1 << uint(idx)) // Set bit
	}
}

/* =====================================================================
   Indexed FIELD STORE - UNLIMITED FIELDS
   ===================================================================== */

// growChunk dynamically adds a new chunk for unlimited field storage
func (eqfs *IndexedFieldStore) growChunk() {
	eqfs.mu.Lock()
	defer eqfs.mu.Unlock()

	// Add new chunk
	eqfs.keys = append(eqfs.keys, make([]string, eqfs.chunkSize))
	eqfs.values = append(eqfs.values, make([]string, eqfs.chunkSize))
	eqfs.bitmasks = append(eqfs.bitmasks, uint64(0))
	eqfs.activeChunks.Add(1)
}

// EnableFastPath enables lock-free fast path operations
func (eqfs *IndexedFieldStore) EnableFastPath() {
	eqfs.fastPath.Store(true)
}

// DisableFastPath disables lock-free fast path operations
func (eqfs *IndexedFieldStore) DisableFastPath() {
	eqfs.fastPath.Store(false)
}

// Set adds or updates a field with chunk-based O(1) performance - LOCK-FREE FAST PATH
func (eqfs *IndexedFieldStore) Set(key, value string) {
	// Fast path: try atomic operations if enabled
	if eqfs.fastPath.Load() {
		if eqfs.setFastPath(key, value) {
			return
		}
	}

	// Slow path: use mutex for safety
	eqfs.setSlowPath(key, value)
}

// setFastPath implements lock-free field setting using atomic operations
func (eqfs *IndexedFieldStore) setFastPath(key, value string) bool {
	fieldID := eqfs.getOrCreateFieldID(key)
	chunkIdx := fieldID / eqfs.chunkSize
	localIdx := fieldID % eqfs.chunkSize

	// Ensure chunk exists (check atomically)
	currentChunks := int(eqfs.activeChunks.Load())
	if chunkIdx >= currentChunks {
		return false // Fall back to slow path for chunk growth
	}

	// Atomic field setting
	if chunkIdx < len(eqfs.keys) && localIdx < len(eqfs.keys[chunkIdx]) {
		eqfs.keys[chunkIdx][localIdx] = key
		eqfs.values[chunkIdx][localIdx] = value
		// Set the presence bit with an atomic OR. The previous code used
		// atomic.Add, which corrupts the mask when a field is re-set (it adds the
		// bit value again, flipping to the wrong bit and hiding the field).
		bit := uint64(1) << uint(localIdx)
		for {
			old := atomic.LoadUint64(&eqfs.bitmasks[chunkIdx])
			if old&bit != 0 {
				break // already set
			}
			if atomic.CompareAndSwapUint64(&eqfs.bitmasks[chunkIdx], old, old|bit) {
				break
			}
		}
		return true
	}

	return false
}

// setSlowPath uses traditional mutex for safety during chunk growth
func (eqfs *IndexedFieldStore) setSlowPath(key, value string) {
	fieldID := eqfs.getOrCreateFieldID(key)
	chunkIdx := fieldID / eqfs.chunkSize
	localIdx := fieldID % eqfs.chunkSize

	// Grow chunks dynamically
	for int(eqfs.activeChunks.Load()) <= chunkIdx {
		eqfs.growChunk()
	}

	eqfs.mu.Lock()
	defer eqfs.mu.Unlock()

	eqfs.keys[chunkIdx][localIdx] = key
	eqfs.values[chunkIdx][localIdx] = value
	eqfs.bitmasks[chunkIdx] |= (1 << uint(localIdx))
}

// SetByID adds or updates a field using a pre-registered field ID for High-performance performance
func (eqfs *IndexedFieldStore) SetByID(fieldID int, value string) {
	chunkIdx := fieldID / eqfs.chunkSize
	localIdx := fieldID % eqfs.chunkSize

	// Grow chunks dynamically
	for int(eqfs.activeChunks.Load()) <= chunkIdx {
		eqfs.growChunk()
	}

	eqfs.mu.Lock()
	defer eqfs.mu.Unlock()

	// Get the key from dictionary for storage consistency
	key := eqfs.dictionary.GetFieldByID(fieldID)
	if key == "" {
		return // Invalid field ID
	}

	eqfs.keys[chunkIdx][localIdx] = key
	eqfs.values[chunkIdx][localIdx] = value
	eqfs.bitmasks[chunkIdx] |= (1 << uint(localIdx))
}

// Get retrieves a field value with chunk-based O(1) performance
func (eqfs *IndexedFieldStore) Get(key string) (string, bool) {
	fieldID, exists := eqfs.getFieldID(key)
	if !exists {
		return "", false
	}

	chunkIdx := fieldID / eqfs.chunkSize
	localIdx := fieldID % eqfs.chunkSize

	if chunkIdx >= len(eqfs.keys) {
		return "", false
	}

	eqfs.mu.RLock()
	defer eqfs.mu.RUnlock()

	if (eqfs.bitmasks[chunkIdx] & (1 << uint(localIdx))) == 0 {
		return "", false
	}

	return eqfs.values[chunkIdx][localIdx], true
}

// GetAll returns all fields as a slice of TypedField - LOCK-FREE FAST PATH
func (eqfs *IndexedFieldStore) GetAll() []TypedFieldData {
	// Fast path: try lock-free access if no concurrent modifications
	if eqfs.fastPath.Load() {
		return eqfs.getAllFastPath()
	}

	// Slow path: use mutex for safety
	return eqfs.getAllSlowPath()
}

// getAllFastPath implements lock-free field retrieval using atomic operations
func (eqfs *IndexedFieldStore) getAllFastPath() []TypedFieldData {
	var result []TypedFieldData

	// Atomic read of current state
	activeChunks := int(eqfs.activeChunks.Load())

	for chunkIdx := 0; chunkIdx < activeChunks && chunkIdx < len(eqfs.keys); chunkIdx++ {
		bitmask := atomic.LoadUint64(&eqfs.bitmasks[chunkIdx])
		if bitmask == 0 {
			continue
		}

		// Process set bits - O(popcount(bits))
		for activeBits := bitmask; activeBits != 0; {
			idx := bits.TrailingZeros64(activeBits)
			activeBits &= activeBits - 1 // Clear lowest set bit

			if idx < len(eqfs.keys[chunkIdx]) && idx < len(eqfs.values[chunkIdx]) {
				key := eqfs.keys[chunkIdx][idx]
				value := eqfs.values[chunkIdx][idx]

				result = append(result, TypedFieldData{
					Key:   key,
					Value: value,
					Type:  TypedFieldString,
				})
			}
		}
	}

	return result
}

// getAllSlowPath uses traditional mutex for safety during concurrent modifications
func (eqfs *IndexedFieldStore) getAllSlowPath() []TypedFieldData {
	eqfs.mu.RLock()
	defer eqfs.mu.RUnlock()

	var result []TypedFieldData

	for chunkIdx := 0; chunkIdx < len(eqfs.keys); chunkIdx++ {
		bitmask := eqfs.bitmasks[chunkIdx]

		for localIdx := 0; localIdx < eqfs.chunkSize; localIdx++ {
			if (bitmask & (1 << uint(localIdx))) != 0 {
				key := eqfs.keys[chunkIdx][localIdx]
				value := eqfs.values[chunkIdx][localIdx]

				result = append(result, TypedFieldData{
					Key:   key,
					Value: value,
					Type:  TypedFieldString,
				})
			}
		}
	}

	return result
}

// Iterate processes all active fields across all chunks
func (eqfs *IndexedFieldStore) Iterate(fn func(key, value string)) {
	eqfs.mu.RLock()
	defer eqfs.mu.RUnlock()

	// Iterate through all chunks
	for chunkIdx, bitmask := range eqfs.bitmasks {
		if bitmask == 0 {
			continue
		}

		// Traverse only set bits in this chunk
		for activeBits := bitmask; activeBits != 0; {
			idx := bits.TrailingZeros64(activeBits)
			activeBits &= activeBits - 1 // Clear lowest set bit

			if idx < len(eqfs.keys[chunkIdx]) {
				fn(eqfs.keys[chunkIdx][idx], eqfs.values[chunkIdx][idx])
			}
		}
	}
}

// TransformValues replaces active values without allocating a merged snapshot.
// The caller must own the store exclusively when lock-free writes are enabled.
// The callback must not call another method on this store or retain mutable state.
func (eqfs *IndexedFieldStore) TransformValues(fn func(key, value string) string) {
	eqfs.mu.Lock()
	defer eqfs.mu.Unlock()
	for chunkIdx, mask := range eqfs.bitmasks {
		for bitsLeft := mask; bitsLeft != 0; bitsLeft &= bitsLeft - 1 {
			i := bits.TrailingZeros64(bitsLeft)
			if i < len(eqfs.keys[chunkIdx]) {
				eqfs.values[chunkIdx][i] = fn(eqfs.keys[chunkIdx][i], eqfs.values[chunkIdx][i])
			}
		}
	}
}

// Reset clears all chunks for reuse
func (eqfs *IndexedFieldStore) Reset() {
	eqfs.mu.Lock()
	defer eqfs.mu.Unlock()

	// Clear all bitmasks
	for i := range eqfs.bitmasks {
		eqfs.bitmasks[i] = 0
	}

	// Keep chunks but clear content
	for i := range eqfs.keys {
		for j := range eqfs.keys[i] {
			eqfs.keys[i][j] = ""
			eqfs.values[i][j] = ""
		}
	}
}

// Size returns the total number of active fields across all chunks
func (eqfs *IndexedFieldStore) Size() int {
	eqfs.mu.RLock()
	defer eqfs.mu.RUnlock()

	total := 0
	for _, bitmask := range eqfs.bitmasks {
		total += bits.OnesCount64(bitmask)
	}
	return total
}

// Helper methods for field ID management (integrated with FieldDictionary)
func (eqfs *IndexedFieldStore) getOrCreateFieldID(key string) int {
	if eqfs.dictionary != nil {
		return eqfs.dictionary.GetOrRegisterFieldID(key)
	}
	// No dictionary: assign small, dense, incremental IDs. Using hashString here
	// previously produced IDs up to ~4 billion, so setSlowPath's chunk-growth
	// loop tried to allocate billions of chunks and never returned.
	eqfs.fallbackMu.Lock()
	defer eqfs.fallbackMu.Unlock()
	if eqfs.fallbackIDs == nil {
		eqfs.fallbackIDs = make(map[string]int)
	}
	if id, ok := eqfs.fallbackIDs[key]; ok {
		return id
	}
	id := len(eqfs.fallbackIDs)
	eqfs.fallbackIDs[key] = id
	return id
}

func (eqfs *IndexedFieldStore) getFieldID(key string) (int, bool) {
	if eqfs.dictionary == nil {
		// Fallback to hash-based field ID generation when dictionary is not available
		return int(hashString(key)), true
	}
	return eqfs.dictionary.GetID(key)
}

// hashString provides a simple hash function for field ID generation
func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
