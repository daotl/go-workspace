// Copyright for portions of this fork are held by [Juan Batiz-Benet, 2016]
// as part of the original go-datastore project. All other copyright for this
// fork are held by [DAOT Labs, 2020]. All rights reserved. Use of this source
// code is governed by MIT license that can be found in the LICENSE file.

package keytransform

import (
	"context"

	ds "github.com/daotl/go-datastore"
	key "github.com/daotl/go-datastore/key"
	dsq "github.com/daotl/go-datastore/query"
)

// Wrap wraps a given datastore with a KeyTransform function.
// The resulting wrapped datastore will use the transform on all Datastore
// operations.
func Wrap(child ds.Datastore, t KeyTransform) *Datastore {
	if t == nil {
		panic("t (KeyTransform) is nil")
	}

	if child == nil {
		panic("child (ds.Datastore) is nil")
	}

	return &Datastore{child: child, KeyTransform: t}
}

// Datastore keeps a KeyTransform function
type Datastore struct {
	child ds.Datastore

	KeyTransform
}

// Children implements ds.Shim
func (d *Datastore) Children() []ds.Datastore {
	return []ds.Datastore{d.child}
}

// Put stores the given value, transforming the key first.
func (d *Datastore) Put(ctx context.Context, key key.Key, value []byte) (err error) {
	return d.child.Put(ctx, d.ConvertKey(key), value)
}

// Sync implements Datastore.Sync
func (d *Datastore) Sync(ctx context.Context, prefix key.Key) error {
	return d.child.Sync(ctx, d.ConvertKey(prefix))
}

// Get returns the value for given key, transforming the key first.
func (d *Datastore) Get(ctx context.Context, key key.Key) (value []byte, err error) {
	return d.child.Get(ctx, d.ConvertKey(key))
}

// Has returns whether the datastore has a value for a given key, transforming
// the key first.
func (d *Datastore) Has(ctx context.Context, key key.Key) (exists bool, err error) {
	return d.child.Has(ctx, d.ConvertKey(key))
}

// GetSize returns the size of the value named by the given key, transforming
// the key first.
func (d *Datastore) GetSize(ctx context.Context, key key.Key) (size int, err error) {
	return d.child.GetSize(ctx, d.ConvertKey(key))
}

// Delete removes the value for given key
func (d *Datastore) Delete(ctx context.Context, key key.Key) (err error) {
	return d.child.Delete(ctx, d.ConvertKey(key))
}

// Query implements Query, inverting keys on the way back out.
func (d *Datastore) Query(ctx context.Context, q dsq.Query) (dsq.Results, error) {
	nq, cq := d.prepareQuery(q)

	cqr, err := d.child.Query(ctx, cq)
	if err != nil {
		return nil, err
	}

	qr := dsq.ResultsFromIterator(q, dsq.Iterator{
		Next: func() (dsq.Result, bool) {
			r, ok := cqr.NextSync()
			if !ok {
				return r, false
			}
			if r.Error == nil {
				r.Entry.Key = d.InvertKey(r.Entry.Key)
			}
			return r, true
		},
		Close: func() error {
			return cqr.Close()
		},
	})
	return dsq.NaiveQueryApply(nq, qr), nil
}

// Split the query into a child query and a naive query. That way, we can make
// the child datastore do as much work as possible.
func (d *Datastore) prepareQuery(q dsq.Query) (naive, child dsq.Query) {

	// First, put everything in the child query. Then, start taking things
	// out.
	child = q

	// Always let the child handle the key prefix.
	child.Prefix = d.ConvertKey(key.Clean(child.Prefix))

	// Always let the child handle the key range.
	child.Range = dsq.Range{}
	if child.Range.Start != nil {
		child.Range.Start = d.ConvertKey(child.Range.Start)
	}
	if child.Range.End != nil {
		child.Range.End = d.ConvertKey(child.Range.End)
	}

	// Check if the key transform is order-preserving so we can use the
	// child datastore's built-in ordering.
	orderPreserving := false
	switch d.KeyTransform.(type) {
	case PrefixTransform, *PrefixTransform:
		orderPreserving = true
	}

	// Try to let the child handle ordering.
orders:
	for i, o := range child.Orders {
		switch o.(type) {
		case dsq.OrderByValue, *dsq.OrderByValue,
			dsq.OrderByValueDescending, *dsq.OrderByValueDescending:
			// Key doesn't matter.
			continue
		case dsq.OrderByKey, *dsq.OrderByKey,
			dsq.OrderByKeyDescending, *dsq.OrderByKeyDescending:
			// if the key transform preserves order, we can delegate
			// to the child datastore.
			if orderPreserving {
				// When sorting, we compare with the first
				// Order, then, if equal, we compare with the
				// second Order, etc. However, keys are _unique_
				// so we'll never apply any additional orders
				// after ordering by key.
				child.Orders = child.Orders[:i+1]
				break orders
			}
		}

		// Can't handle this order under transform, punt it to a naive
		// ordering.
		naive.Orders = q.Orders
		child.Orders = nil
		naive.Offset = q.Offset
		child.Offset = 0
		naive.Limit = q.Limit
		child.Limit = 0
		break
	}

	// Try to let the child handle the filters.

	// don't modify the original filters.
	child.Filters = append([]dsq.Filter(nil), child.Filters...)

	for i, f := range child.Filters {
		switch f := f.(type) {
		case dsq.FilterValueCompare, *dsq.FilterValueCompare:
			continue
		case dsq.FilterKeyCompare:
			child.Filters[i] = dsq.FilterKeyCompare{
				Op:  f.Op,
				Key: d.ConvertKey(f.Key),
			}
			continue
		case *dsq.FilterKeyCompare:
			child.Filters[i] = &dsq.FilterKeyCompare{
				Op:  f.Op,
				Key: d.ConvertKey(f.Key),
			}
			continue
		case dsq.FilterKeyPrefix:
			child.Filters[i] = dsq.FilterKeyPrefix{
				Prefix: d.ConvertKey(f.Prefix),
			}
			continue
		case *dsq.FilterKeyPrefix:
			child.Filters[i] = &dsq.FilterKeyPrefix{
				Prefix: d.ConvertKey(f.Prefix),
			}
			continue
		}

		// Not a known filter, defer to the naive implementation.
		naive.Filters = q.Filters
		child.Filters = nil
		naive.Offset = q.Offset
		child.Offset = 0
		naive.Limit = q.Limit
		child.Limit = 0
		break
	}
	return
}

func (d *Datastore) Close() error {
	return d.child.Close()
}

// DiskUsage implements the PersistentDatastore interface.
func (d *Datastore) DiskUsage(ctx context.Context) (uint64, error) {
	return ds.DiskUsage(ctx, d.child)
}

func (d *Datastore) Batch(ctx context.Context) (ds.Batch, error) {
	bds, ok := d.child.(ds.Batching)
	if !ok {
		return nil, ds.ErrBatchUnsupported
	}

	childbatch, err := bds.Batch(ctx)
	if err != nil {
		return nil, err
	}
	return &transformBatch{
		dst: childbatch,
		f:   d.ConvertKey,
	}, nil
}

type transformBatch struct {
	dst ds.Batch

	f KeyMapping
}

func (t *transformBatch) Put(ctx context.Context, key key.Key, val []byte) error {
	return t.dst.Put(ctx, t.f(key), val)
}

func (t *transformBatch) Delete(ctx context.Context, key key.Key) error {
	return t.dst.Delete(ctx, t.f(key))
}

func (t *transformBatch) Commit(ctx context.Context) error {
	return t.dst.Commit(ctx)
}

func (d *Datastore) Check(ctx context.Context) error {
	if c, ok := d.child.(ds.CheckedDatastore); ok {
		return c.Check(ctx)
	}
	return nil
}

func (d *Datastore) Scrub(ctx context.Context) error {
	if c, ok := d.child.(ds.ScrubbedDatastore); ok {
		return c.Scrub(ctx)
	}
	return nil
}

func (d *Datastore) CollectGarbage(ctx context.Context) error {
	if c, ok := d.child.(ds.GCDatastore); ok {
		return c.CollectGarbage(ctx)
	}
	return nil
}

var _ ds.Datastore = (*Datastore)(nil)
var _ ds.GCDatastore = (*Datastore)(nil)
var _ ds.Batching = (*Datastore)(nil)
var _ ds.PersistentDatastore = (*Datastore)(nil)
var _ ds.ScrubbedDatastore = (*Datastore)(nil)
