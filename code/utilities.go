package jr_cache

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/gob"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EvictionPolicy represents the policy used for evicting entries from a cache.
type EvictionPolicy int

const (
	LIFO EvictionPolicy = iota
	FIFO
	LFU
	MFU
	LRU
	MRU
)

// IsValid reports whether p is one of the declared eviction policies.
func (p EvictionPolicy) IsValid() bool {
	return p >= LIFO && p <= MRU
}

func (p EvictionPolicy) String() string {
	switch p {
	case LIFO:
		return "LIFO"
	case FIFO:
		return "FIFO"
	case LFU:
		return "LFU"
	case MFU:
		return "MFU"
	case LRU:
		return "LRU"
	case MRU:
		return "MRU"
	}
	return "unknown"
}

// Errors related to serialization.
var (
	ErrNilData                = errors.New("data is nil")
	ErrEmptyData              = errors.New("data is empty")
	ErrUnsupportedCompression = errors.New("unsupported compression algorithm")
	ErrUnsupportedFormat      = errors.New("unsupported serialization format")
	ErrNilSnapshot            = errors.New("snapshot is nil")
)

// CompressionAlgorithm represents the algorithm used for compressing data.
type CompressionAlgorithm int

const (
	NoCompression CompressionAlgorithm = iota
	GzipCompression
	ZlibCompression
)

// IsValid reports whether p is one of the declared compression algorithms.
func (p CompressionAlgorithm) IsValid() bool {
	return p >= NoCompression && p <= ZlibCompression
}

func (p CompressionAlgorithm) String() string {
	switch p {
	case NoCompression:
		return "NoCompression"
	case GzipCompression:
		return "GzipCompression"
	case ZlibCompression:
		return "ZlibCompression"
	}
	return "unknown"
}

// SerializationFormat selects the encoding used by Serialize and Deserialize.
type SerializationFormat int

const (
	// JSONFormat is portable and readable, but it cannot encode map keys that
	// are neither strings, integers nor encoding.TextMarshaler, and it loses
	// the concrete type of a value held in an interface.
	JSONFormat SerializationFormat = iota
	// GobFormat is Go-native: it handles struct keys and keeps exact types.
	// A value held in an interface must be registered with gob.Register.
	GobFormat
)

// IsValid reports whether f is one of the declared formats.
func (f SerializationFormat) IsValid() bool {
	return f >= JSONFormat && f <= GobFormat
}

func (f SerializationFormat) String() string {
	switch f {
	case JSONFormat:
		return "JSONFormat"
	case GobFormat:
		return "GobFormat"
	}
	return "unknown"
}

// Entry is one key/value pair. Snapshots hold entries in a slice rather than
// a map, so that the order is preserved and so that any comparable key type
// can be encoded — which a JSON object key cannot.
type Entry[Key comparable, Value any] struct {
	Key   Key   `json:"key"`
	Value Value `json:"value"`
}

// CacheEntry is an Entry plus the per-entry state a cache keeps: the bucket
// index under the frequency policies (0 for the recency ones) and the expiry
// deadline (the zero time when the entry has no TTL).
type CacheEntry[Key comparable, Value any] struct {
	Key       Key       `json:"key"`
	Value     Value     `json:"value"`
	Frequency uint64    `json:"frequency,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// CommonMapInterface is the API shared by every map in this package. The
// ordered map, the bucket map and the cache map all expose these methods with
// the same names and signatures, so callers can switch between them freely.
//
// "Top" and "Bottom" are the two ends of whatever order a map keeps; for the
// bucket map that is the lowest and highest index, for the ordered map the
// head and the tail.
type CommonMapInterface[Key comparable, Value any] interface {
	ToMap() map[Key]Value
	FromMap(entries map[Key]Value)
	Get(key Key) (Value, bool)
	Has(key Key) bool
	Update(key Key, value Value) bool
	Pop(key Key) (Value, bool)
	Delete(key Key)
	Top() (Key, Value, bool)
	Bottom() (Key, Value, bool)
	TopKey() (Key, bool)
	BottomKey() (Key, bool)
	PopTop() (Key, Value, bool)
	PopBottom() (Key, Value, bool)
	Len() int
	Clear()
}

// Helpers shared by the sharded structures.

// partition_by_shard groups entries by the shard index shardOf assigns to
// their key, so each shard can be filled in one go.
func partition_by_shard[Key comparable, Value any](entries map[Key]Value, shards int, shardOf func(Key) int) []map[Key]Value {
	parts := make([]map[Key]Value, shards)
	for key, value := range entries {
		index := shardOf(key)
		if parts[index] == nil {
			parts[index] = make(map[Key]Value, len(entries)/shards+1)
		}
		parts[index][key] = value
	}
	return parts
}

// run_per_shard hands each non-empty partition to one of up to threads
// workers; a shard is only ever handled by a single worker at a time.
func run_per_shard[Key comparable, Value any](parts []map[Key]Value, threads int, fn func(index int, part map[Key]Value)) {
	if threads <= 0 {
		threads = 1
	}
	if threads > len(parts) {
		threads = len(parts)
	}
	work := make(chan int, len(parts))
	for index, part := range parts {
		if len(part) > 0 {
			work <- index
		}
	}
	close(work)

	var wg sync.WaitGroup
	for i := 0; i < threads; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range work {
				fn(index, parts[index])
			}
		}()
	}
	wg.Wait()
}

// Serialization and deserialization, with optional compression.
//
// The pipeline is: encode (JSON or gob), then compress. Deserialize runs it
// backwards — decompress, then decode — so the two are exact inverses.

// Compression helpers.

func decompressFromGzip(data []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}

func decompressFromZlib(data []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// decompress reverses compress. An unknown algorithm is an error rather than
// a silent pass-through, so a mismatched call cannot hand back bytes that are
// still compressed.
func decompress(data []byte, algorithm CompressionAlgorithm) ([]byte, error) {
	switch algorithm {
	case NoCompression:
		return data, nil
	case GzipCompression:
		return decompressFromGzip(data)
	case ZlibCompression:
		return decompressFromZlib(data)
	default:
		return nil, ErrUnsupportedCompression
	}
}

func compressToGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	// Close writes the trailer; the bytes are only complete afterwards.
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compressToZlib(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func compress(data []byte, algorithm CompressionAlgorithm) ([]byte, error) {
	switch algorithm {
	case NoCompression:
		return data, nil
	case GzipCompression:
		return compressToGzip(data)
	case ZlibCompression:
		return compressToZlib(data)
	default:
		return nil, ErrUnsupportedCompression
	}
}

// Encoding helpers.

func encode[Data any](data Data, format SerializationFormat) ([]byte, error) {
	switch format {
	case JSONFormat:
		return json.Marshal(data)
	case GobFormat:
		var buf bytes.Buffer
		if err := gob.NewEncoder(&buf).Encode(data); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default:
		return nil, ErrUnsupportedFormat
	}
}

func decode[Data any](raw []byte, format SerializationFormat) (Data, error) {
	var data Data
	switch format {
	case JSONFormat:
		if err := json.Unmarshal(raw, &data); err != nil {
			return *new(Data), err
		}
		return data, nil
	case GobFormat:
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(&data); err != nil {
			return *new(Data), err
		}
		return data, nil
	default:
		return *new(Data), ErrUnsupportedFormat
	}
}

// Public serialization API.

// Serialize encodes data in the given format and compresses the result with
// the given algorithm. Deserialize with the same pair reverses it.
func Serialize[Data any](data Data, format SerializationFormat, algorithm CompressionAlgorithm) ([]byte, error) {
	raw, err := encode(data, format)
	if err != nil {
		return nil, err
	}
	return compress(raw, algorithm)
}

// Deserialize decompresses dataBytes and decodes it back into Data. The
// format and algorithm must match the ones Serialize was called with.
func Deserialize[Data any](dataBytes []byte, format SerializationFormat, algorithm CompressionAlgorithm) (Data, error) {
	if dataBytes == nil {
		return *new(Data), ErrNilData
	}
	if len(dataBytes) == 0 {
		return *new(Data), ErrEmptyData
	}
	raw, err := decompress(dataBytes, algorithm)
	if err != nil {
		return *new(Data), err
	}
	return decode[Data](raw, format)
}

// DefaultFileMode is the permission SaveToFile gives a file it creates.
const DefaultFileMode os.FileMode = 0o644

// SaveToFile serializes data and writes it to filename. The write goes to a
// temporary file in the same directory and is then renamed over the target,
// so an interrupted save cannot leave a half-written file behind. Pass nil
// for mode to use DefaultFileMode.
func SaveToFile[Data any](data Data, filename string, format SerializationFormat, algorithm CompressionAlgorithm, mode *os.FileMode) error {
	dataBytes, err := Serialize(data, format, algorithm)
	if err != nil {
		return err
	}
	perm := DefaultFileMode
	if mode != nil {
		perm = *mode
	}

	temp, err := os.CreateTemp(filepath.Dir(filename), filepath.Base(filename)+".tmp*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	// A no-op once the rename below has succeeded.
	defer os.Remove(tempName)

	if _, err := temp.Write(dataBytes); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempName, perm); err != nil {
		return err
	}
	return os.Rename(tempName, filename)
}

// LoadFromFile reads filename and deserializes its contents.
func LoadFromFile[Data any](filename string, format SerializationFormat, algorithm CompressionAlgorithm) (Data, error) {
	dataBytes, err := os.ReadFile(filename)
	if err != nil {
		return *new(Data), err
	}
	return Deserialize[Data](dataBytes, format, algorithm)
}
