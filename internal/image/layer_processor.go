package image

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/so2liu/imgcd/internal/cache"
)

// PreparedLayer represents a layer that has been downloaded and prepared for tar writing
type PreparedLayer struct {
	Index  int
	DiffID string
	Digest string
	Data   *bytes.Buffer
	Size   int64
	Err    error
}

// LayerProcessor handles parallel processing of image layers
type LayerProcessor struct {
	workers         int
	bufferSize      int
	layerCache      *cache.LayerCache
	orderedBuf      *orderedBuffer
	imageRef        string
	downloadedCount int
	totalLayers     int
	mu              sync.Mutex
}

// orderedBuffer ensures layers are output in correct order
type orderedBuffer struct {
	bufferSize int
	buffer     map[int]*PreparedLayer
	nextIndex  int
	outputChan chan *PreparedLayer
	doneChan   chan struct{}
	mu         sync.Mutex
	wg         sync.WaitGroup
}

// NewLayerProcessor creates a new layer processor
func NewLayerProcessor(layerCache *cache.LayerCache, imageRef string, totalLayers int) *LayerProcessor {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8 // Limit maximum workers
	}
	if workers < 2 {
		workers = 2 // Minimum 2 workers
	}

	bufferSize := 4 // Keep max 4 layers in memory

	return &LayerProcessor{
		workers:     workers,
		bufferSize:  bufferSize,
		layerCache:  layerCache,
		imageRef:    imageRef,
		totalLayers: totalLayers,
		orderedBuf: &orderedBuffer{
			bufferSize: bufferSize,
			buffer:     make(map[int]*PreparedLayer),
			nextIndex:  0,
			outputChan: make(chan *PreparedLayer, bufferSize),
			doneChan:   make(chan struct{}),
		},
	}
}

// ProcessLayers processes all layers in parallel and returns a channel for ordered output
func (lp *LayerProcessor) ProcessLayers(ctx context.Context, layers []v1.Layer) <-chan *PreparedLayer {
	fmt.Fprintf(os.Stderr, "Processing %d layers in parallel (using %d workers)...\n",
		len(layers), lp.workers)

	// Start ordered buffer goroutine
	go lp.orderedBuf.run()

	// Create worker pool
	workChan := make(chan layerWork, len(layers))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < lp.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			lp.worker(ctx, workChan)
		}(i)
	}

	// Send work to workers
	go func() {
		for idx, layer := range layers {
			workChan <- layerWork{
				index: idx,
				layer: layer,
			}
		}
		close(workChan)

		// Wait for all workers to finish
		wg.Wait()

		// Signal ordered buffer we're done
		lp.orderedBuf.finish()
	}()

	return lp.orderedBuf.outputChan
}

type layerWork struct {
	index int
	layer v1.Layer
}

// worker processes layers from the work channel
func (lp *LayerProcessor) worker(ctx context.Context, workChan <-chan layerWork) {
	for work := range workChan {
		prepared := lp.processLayer(ctx, work.index, work.layer)
		lp.orderedBuf.add(prepared)

		// Update progress
		lp.mu.Lock()
		lp.downloadedCount++
		fmt.Fprintf(os.Stderr, "Prepared layer %d/%d\r", lp.downloadedCount, lp.totalLayers)
		lp.mu.Unlock()
	}
}

// processLayer downloads or retrieves a layer from cache
func (lp *LayerProcessor) processLayer(ctx context.Context, index int, layer v1.Layer) *PreparedLayer {
	diffID, err := layer.DiffID()
	if err != nil {
		return &PreparedLayer{Index: index, Err: fmt.Errorf("failed to get DiffID: %w", err)}
	}

	digest, err := layer.Digest()
	if err != nil {
		return &PreparedLayer{Index: index, Err: fmt.Errorf("failed to get digest: %w", err)}
	}

	size, err := layer.Size()
	if err != nil {
		return &PreparedLayer{Index: index, Err: fmt.Errorf("failed to get size: %w", err)}
	}

	// Check cache first
	if lp.layerCache.Exists(diffID.String()) {
		cachedReader, err := lp.layerCache.Get(diffID.String())
		if err == nil {
			// Read from cache to memory buffer
			buf := &bytes.Buffer{}
			_, err = io.Copy(buf, cachedReader)
			cachedReader.Close()

			if err == nil {
				return &PreparedLayer{
					Index:  index,
					DiffID: diffID.String(),
					Digest: digest.String(),
					Data:   buf,
					Size:   size,
				}
			}
		}
		// Cache read failed, fall through to download
	}

	// Download layer
	layerReader, err := layer.Uncompressed()
	if err != nil {
		return &PreparedLayer{Index: index, Err: fmt.Errorf("failed to get layer content: %w", err)}
	}
	defer layerReader.Close()

	// Read layer data to buffer
	buf := &bytes.Buffer{}
	_, err = io.Copy(buf, layerReader)
	if err != nil {
		return &PreparedLayer{Index: index, Err: fmt.Errorf("failed to read layer: %w", err)}
	}

	// Asynchronously write to cache (don't block)
	go func() {
		reader := bytes.NewReader(buf.Bytes())
		lp.layerCache.Put(diffID.String(), reader, lp.imageRef, size)
	}()

	return &PreparedLayer{
		Index:  index,
		DiffID: diffID.String(),
		Digest: digest.String(),
		Data:   buf,
		Size:   size,
	}
}

// orderedBuffer methods

func (ob *orderedBuffer) run() {
	ob.wg.Add(1)
	go func() {
		defer ob.wg.Done()
		<-ob.doneChan

		// Output any remaining buffered layers in order
		ob.mu.Lock()
		for {
			if layer, exists := ob.buffer[ob.nextIndex]; exists {
				ob.outputChan <- layer
				delete(ob.buffer, ob.nextIndex)
				ob.nextIndex++
			} else {
				break
			}
		}
		ob.mu.Unlock()

		close(ob.outputChan)
	}()
}

func (ob *orderedBuffer) add(layer *PreparedLayer) {
	ob.mu.Lock()
	defer ob.mu.Unlock()

	// Add to buffer
	ob.buffer[layer.Index] = layer

	// Output layers in order if they're ready
	for {
		if layer, exists := ob.buffer[ob.nextIndex]; exists {
			// Check if buffer is getting full - block if needed
			if len(ob.buffer) >= ob.bufferSize {
				// Send to output (this will block if channel is full)
				ob.mu.Unlock()
				ob.outputChan <- layer
				ob.mu.Lock()
			} else {
				ob.outputChan <- layer
			}

			delete(ob.buffer, ob.nextIndex)
			ob.nextIndex++
		} else {
			break
		}
	}
}

func (ob *orderedBuffer) finish() {
	close(ob.doneChan)
	ob.wg.Wait()
}
