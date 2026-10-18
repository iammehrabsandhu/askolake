// Package index reads and writes the index bundle shared by seeder and server.
package index

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"olake-ask/internal/domain"
)

// Bundle file names.
const (
	ManifestFile = "manifest.json"
	ChunksFile   = "chunks.jsonl.gz"
	VectorsFile  = "vectors.bin"
	PiecesFile   = "pieces.json"
	// APIVectorsFile holds optional second vectors (Manifest.APIEmbedder)
	// over the same pieces as VectorsFile.
	APIVectorsFile = "vectors_api.bin"
	readmeFile     = "README.md"
	schema         = 1
)

// Bundle is a fully loaded index.
type Bundle struct {
	Manifest domain.Manifest
	Chunks   []domain.Chunk
	Vectors  []float32 // row-major Pieces × Dim
	Pieces   []int     // chunk ID per vector row
	// APIVectors is row-major Pieces × APIEmbedder.Dim; nil when absent.
	APIVectors []float32
}

// Write atomically replaces the bundle in dir. It fills Schema, Chunks,
// Embedder.Pieces and (when empty) Version in m.
func Write(dir string, m domain.Manifest, chunks []domain.Chunk, vecs [][]float32, pieces []int) error {
	m.APIEmbedder = nil
	return WriteWithAPI(dir, m, chunks, vecs, pieces, nil)
}

// WriteWithAPI is Write plus optional second vectors apiVecs, described by
// m.APIEmbedder and aligned with pieces. It requires m.Embedder.
func WriteWithAPI(dir string, m domain.Manifest, chunks []domain.Chunk, vecs [][]float32, pieces []int, apiVecs [][]float32) error {
	for i, c := range chunks {
		if c.ID != i {
			return fmt.Errorf("index write: chunk %d has ID %d (IDs must be dense 0..n-1)", i, c.ID)
		}
	}
	m.Schema = schema
	m.Chunks = len(chunks)
	if m.Embedder != nil {
		if len(vecs) != len(pieces) {
			return fmt.Errorf("index write: %d vectors but %d piece owners", len(vecs), len(pieces))
		}
		for i, v := range vecs {
			if len(v) != m.Embedder.Dim {
				return fmt.Errorf("index write: vector %d has dim %d, want %d", i, len(v), m.Embedder.Dim)
			}
			if pieces[i] < 0 || pieces[i] >= len(chunks) {
				return fmt.Errorf("index write: piece %d owner %d out of range", i, pieces[i])
			}
		}
		e := *m.Embedder
		e.Pieces = len(pieces)
		m.Embedder = &e
	}
	if m.APIEmbedder == nil && apiVecs != nil {
		return fmt.Errorf("index write: API vectors without an API embedder")
	}
	if m.APIEmbedder != nil {
		if m.Embedder == nil {
			return fmt.Errorf("index write: API vectors require local vectors")
		}
		if len(apiVecs) != len(pieces) {
			return fmt.Errorf("index write: %d API vectors but %d piece owners", len(apiVecs), len(pieces))
		}
		for i, v := range apiVecs {
			if len(v) != m.APIEmbedder.Dim {
				return fmt.Errorf("index write: API vector %d has dim %d, want %d", i, len(v), m.APIEmbedder.Dim)
			}
		}
		e := *m.APIEmbedder
		e.Pieces = len(pieces)
		m.APIEmbedder = &e
	}

	chunksGz, err := encodeChunks(chunks)
	if err != nil {
		return err
	}
	if m.Version == "" {
		sum := sha256.Sum256(chunksGz)
		m.Version = m.BuiltAt.UTC().Format("20060102T1504Z") + "-" + hex.EncodeToString(sum[:])[:8]
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("index write: encode manifest: %w", err)
	}

	files := map[string][]byte{ChunksFile: chunksGz}
	if m.Embedder != nil {
		files[VectorsFile] = encodeVectors(vecs, m.Embedder.Dim)
		if m.APIEmbedder != nil {
			files[APIVectorsFile] = encodeVectors(apiVecs, m.APIEmbedder.Dim)
		}
		p, err := json.Marshal(pieces)
		if err != nil {
			return fmt.Errorf("index write: encode pieces: %w", err)
		}
		files[PiecesFile] = p
	}
	files[ManifestFile] = append(manifest, '\n')

	tmp := filepath.Clean(dir) + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("index write: clear %s: %w", tmp, err)
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("index write: %w", err)
	}
	for name, b := range files {
		if err := writeSync(filepath.Join(tmp, name), b); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("index write: %w", err)
	}
	// Remove the manifest first so an interrupted swap reads as "invalid", never as inconsistent.
	if err := os.Remove(filepath.Join(dir, ManifestFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("index write: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("index write: %w", err)
	}
	for _, e := range entries {
		if e.Name() == readmeFile {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("index write: remove stale %s: %w", e.Name(), err)
		}
	}
	for name := range files {
		if name == ManifestFile {
			continue
		}
		if err := os.Rename(filepath.Join(tmp, name), filepath.Join(dir, name)); err != nil {
			return fmt.Errorf("index write: move %s: %w", name, err)
		}
	}
	if err := os.Rename(filepath.Join(tmp, ManifestFile), filepath.Join(dir, ManifestFile)); err != nil {
		return fmt.Errorf("index write: move manifest: %w", err)
	}
	syncDir(dir)
	if err := os.RemoveAll(tmp); err != nil {
		return fmt.Errorf("index write: cleanup %s: %w", tmp, err)
	}
	return nil
}

func encodeVectors(vecs [][]float32, dim int) []byte {
	buf := make([]byte, 0, len(vecs)*dim*4)
	for _, v := range vecs {
		for _, f := range v {
			buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(f))
		}
	}
	return buf
}

func encodeChunks(chunks []domain.Chunk) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	for _, c := range chunks {
		if err := enc.Encode(c); err != nil {
			return nil, fmt.Errorf("index write: encode chunk %d: %w", c.ID, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("index write: gzip: %w", err)
	}
	return buf.Bytes(), nil
}

func writeSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("index write: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("index write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("index write: fsync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("index write: close %s: %w", path, err)
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best effort; not supported on every platform
		d.Close()
	}
}

// ReadManifest reads only manifest.json.
func ReadManifest(fsys fs.FS) (domain.Manifest, error) {
	var m domain.Manifest
	b, err := fs.ReadFile(fsys, ManifestFile)
	if err != nil {
		return m, fmt.Errorf("read %s: %w", ManifestFile, err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("parse %s: %w", ManifestFile, err)
	}
	if m.Schema != schema {
		return m, fmt.Errorf("%s: schema %d, want %d", ManifestFile, m.Schema, schema)
	}
	return m, nil
}

// Read loads and validates a bundle.
func Read(fsys fs.FS) (Bundle, error) {
	var b Bundle
	m, err := ReadManifest(fsys)
	if err != nil {
		return b, err
	}
	b.Manifest = m
	if b.Chunks, err = readChunks(fsys); err != nil {
		return b, err
	}
	if len(b.Chunks) != m.Chunks {
		return b, fmt.Errorf("%s has %d chunks, manifest says %d", ChunksFile, len(b.Chunks), m.Chunks)
	}
	for i, c := range b.Chunks {
		if c.ID != i {
			return b, fmt.Errorf("%s: line %d has id %d (ids must be dense)", ChunksFile, i+1, c.ID)
		}
	}
	if m.Embedder == nil {
		return b, nil
	}
	if m.Embedder.Dim <= 0 {
		return b, fmt.Errorf("manifest embedder dim %d is invalid", m.Embedder.Dim)
	}
	raw, err := fs.ReadFile(fsys, VectorsFile)
	if err != nil {
		return b, fmt.Errorf("read %s: %w", VectorsFile, err)
	}
	if len(raw)%4 != 0 {
		return b, fmt.Errorf("%s: size %d is not a multiple of 4", VectorsFile, len(raw))
	}
	b.Vectors = make([]float32, len(raw)/4)
	for i := range b.Vectors {
		b.Vectors[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	pj, err := fs.ReadFile(fsys, PiecesFile)
	if err != nil {
		return b, fmt.Errorf("read %s: %w", PiecesFile, err)
	}
	if err := json.Unmarshal(pj, &b.Pieces); err != nil {
		return b, fmt.Errorf("parse %s: %w", PiecesFile, err)
	}
	if len(b.Pieces) != m.Embedder.Pieces {
		return b, fmt.Errorf("%s has %d entries, manifest says %d pieces", PiecesFile, len(b.Pieces), m.Embedder.Pieces)
	}
	if len(b.Vectors) != m.Embedder.Pieces*m.Embedder.Dim {
		return b, fmt.Errorf("%s has %d floats, want pieces×dim = %d×%d", VectorsFile, len(b.Vectors), m.Embedder.Pieces, m.Embedder.Dim)
	}
	for i, o := range b.Pieces {
		if o < 0 || o >= len(b.Chunks) {
			return b, fmt.Errorf("%s: entry %d references chunk %d out of range", PiecesFile, i, o)
		}
	}
	// The API set is optional: if it is broken, drop it and keep the local index.
	if m.APIEmbedder != nil {
		if b.APIVectors, err = readAPIVectors(fsys, *m.APIEmbedder, len(b.Pieces)); err != nil {
			slog.Warn("index: API vectors ignored", "err", err)
			b.APIVectors = nil
			b.Manifest.APIEmbedder = nil
		}
	}
	return b, nil
}

func readAPIVectors(fsys fs.FS, e domain.EmbedInfo, pieces int) ([]float32, error) {
	if e.Dim <= 0 || e.Pieces != pieces {
		return nil, fmt.Errorf("manifest api_embedder dim %d / pieces %d invalid (want %d pieces)", e.Dim, e.Pieces, pieces)
	}
	raw, err := fs.ReadFile(fsys, APIVectorsFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", APIVectorsFile, err)
	}
	if len(raw) != pieces*e.Dim*4 {
		return nil, fmt.Errorf("%s: size %d, want pieces×dim×4 = %d", APIVectorsFile, len(raw), pieces*e.Dim*4)
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out, nil
}

func readChunks(fsys fs.FS) ([]domain.Chunk, error) {
	f, err := fsys.Open(ChunksFile)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", ChunksFile, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ChunksFile, err)
	}
	defer zr.Close()
	dec := json.NewDecoder(zr)
	var out []domain.Chunk
	for {
		var c domain.Chunk
		if err := dec.Decode(&c); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%s: decode chunk %d: %w", ChunksFile, len(out), err)
		}
		out = append(out, c)
	}
	return out, nil
}
