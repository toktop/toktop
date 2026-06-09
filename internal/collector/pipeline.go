package collector

import (
	"context"
	"fmt"

	"github.com/sourcegraph/conc/iter"

	"toktop.unceas.dev/internal/source"
)

func SafeMapErr[T, R any](ctx context.Context, items []T, fn func(*T) (R, error)) ([]R, error) {
	return iter.MapErr(items, func(t *T) (r R, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				err = fmt.Errorf("worker panicked: %v", rec)
			}
		}()
		if err := ctx.Err(); err != nil {
			return r, err
		}
		return fn(t)
	})
}

func ChunkBySize[T any](items []T, sizeOf func(T) int64, maxBytes int64) [][]T {
	if len(items) == 0 {
		return nil
	}
	var batches [][]T
	var cur []T
	var curBytes int64
	for _, item := range items {
		size := sizeOf(item)
		if len(cur) > 0 && curBytes+size > maxBytes {
			batches = append(batches, cur)
			cur = nil
			curBytes = 0
		}
		cur = append(cur, item)
		curBytes += size
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}

func ReleaseRawJSON(events []source.RawEvent) {
	for i := range events {
		events[i].RawJSON = nil
	}
}

func PartitionByFingerprint[F any](candidates []F, path func(F) string, known map[string]source.Fingerprint) (changed []F, skipped []string, fingerprints map[string]source.Fingerprint) {
	changed = make([]F, 0, len(candidates))
	skipped = make([]string, 0)
	fingerprints = make(map[string]source.Fingerprint, len(candidates))
	for _, f := range candidates {
		p := path(f)
		size, mtimeNS, ino, ok := StatFingerprint(p)
		if !ok {
			changed = append(changed, f)
			continue
		}
		fp := source.Fingerprint{Size: size, MtimeNS: mtimeNS, Ino: ino}
		fingerprints[p] = fp
		if prior, hadPrior := known[p]; hadPrior && prior == fp {
			skipped = append(skipped, p)
			continue
		}
		changed = append(changed, f)
	}
	return changed, skipped, fingerprints
}
