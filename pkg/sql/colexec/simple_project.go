// Copyright 2018 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package colexec

import (
	"context"

	"github.com/cockroachdb/cockroach/pkg/col/coldata"
	"github.com/cockroachdb/cockroach/pkg/col/coltypes"
	"github.com/cockroachdb/cockroach/pkg/sql/colexec/execerror"
	"github.com/cockroachdb/cockroach/pkg/util/log"
)

// simpleProjectOp is an operator that implements "simple projection" - removal of
// columns that aren't needed by later operators.
type simpleProjectOp struct {
	OneInputNode
	NonExplainable

	projection []uint32
	batches    map[coldata.Batch]*projectingBatch
	// numBatchesLoggingThreshold is the threshold on the number of items in
	// 'batches' map at which we will log a message when a new projectingBatch
	// is created. It is growing exponentially.
	numBatchesLoggingThreshold int
}

var _ Operator = &simpleProjectOp{}

// projectingBatch is a Batch that applies a simple projection to another,
// underlying batch, discarding all columns but the ones in its projection
// slice, in order.
type projectingBatch struct {
	coldata.Batch

	projection []uint32
}

func newProjectionBatch(projection []uint32) *projectingBatch {
	p := &projectingBatch{
		projection: make([]uint32, len(projection)),
	}
	// We make a copy of projection to be safe.
	copy(p.projection, projection)
	return p
}

func (b *projectingBatch) ColVec(i int) coldata.Vec {
	return b.Batch.ColVec(int(b.projection[i]))
}

func (b *projectingBatch) ColVecs() []coldata.Vec {
	execerror.VectorizedInternalPanic("projectingBatch doesn't support ColVecs()")
	// This code is unreachable, but the compiler cannot infer that.
	return nil
}

func (b *projectingBatch) Width() int {
	return len(b.projection)
}

func (b *projectingBatch) AppendCol(t coltypes.T) {
	b.Batch.AppendCol(t)
	b.projection = append(b.projection, uint32(b.Batch.Width())-1)
}

// NewSimpleProjectOp returns a new simpleProjectOp that applies a simple
// projection on the columns in its input batch, returning a new batch with
// only the columns in the projection slice, in order. In a degenerate case
// when input already outputs batches that satisfy the projection, a
// simpleProjectOp is not planned and input is returned.
func NewSimpleProjectOp(input Operator, numInputCols int, projection []uint32) Operator {
	if numInputCols == len(projection) {
		projectionIsRedundant := true
		for i := range projection {
			if projection[i] != uint32(i) {
				projectionIsRedundant = false
			}
		}
		if projectionIsRedundant {
			return input
		}
	}
	s := &simpleProjectOp{
		OneInputNode:               NewOneInputNode(input),
		projection:                 make([]uint32, len(projection)),
		batches:                    make(map[coldata.Batch]*projectingBatch),
		numBatchesLoggingThreshold: 128,
	}
	// We make a copy of projection to be safe.
	copy(s.projection, projection)
	return s
}

func (d *simpleProjectOp) Init() {
	d.input.Init()
}

func (d *simpleProjectOp) Next(ctx context.Context) coldata.Batch {
	batch := d.input.Next(ctx)
	projBatch, found := d.batches[batch]
	if !found {
		projBatch = newProjectionBatch(d.projection)
		d.batches[batch] = projBatch
		if len(d.batches) == d.numBatchesLoggingThreshold {
			if log.V(1) {
				log.Infof(ctx, "simpleProjectOp: size of 'batches' map = %d", len(d.batches))
			}
			d.numBatchesLoggingThreshold = d.numBatchesLoggingThreshold * 2
		}
	}
	projBatch.Batch = batch
	return projBatch
}