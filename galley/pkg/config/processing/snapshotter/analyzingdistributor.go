// Copyright 2019 Istio Authors
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

package snapshotter

import (
	"sync"

	"istio.io/istio/galley/pkg/config/analysis"
	"istio.io/istio/galley/pkg/config/analysis/diag"
	coll "istio.io/istio/galley/pkg/config/collection"
	"istio.io/istio/galley/pkg/config/resource"
	"istio.io/istio/galley/pkg/config/schema/collection"
	"istio.io/istio/galley/pkg/config/scope"
)

// CollectionReporterFn is a hook function called whenever a collection is accessed through the AnalyzingDistributor's context
type CollectionReporterFn func(collection.Name)

// AnalyzingDistributor is an snapshotter. Distributor implementation that will perform analysis on a snapshot before
// publishing. It will update the CRD status with the analysis results.
type AnalyzingDistributor struct {
	s AnalyzingDistributorSettings

	analysisMu     sync.Mutex
	cancelAnalysis chan struct{}

	snapshotsMu   sync.RWMutex
	lastSnapshots map[string]*Snapshot
}

var _ Distributor = &AnalyzingDistributor{}

// AnalyzingDistributorSettings are settings for an AnalyzingDistributor
type AnalyzingDistributorSettings struct {
	// The status updater to route diagnostic messages to
	StatusUpdater StatusUpdater

	// The top-level combined analyzer that will perform the analysis
	Analyzer *analysis.CombinedAnalyzer

	// The downstream distributor to call, after the analysis is done.
	Distributor Distributor

	// The snapshots that will get analyzed.
	AnalysisSnapshots []string

	// The snapshot that will trigger the analysis.
	// TODO(https://github.com/istio/istio/issues/17543): This should be eventually replaced by the AnalysisSnapshots
	//  and a matching debounce mechanism.
	TriggerSnapshot string

	// An optional hook that will be called whenever a collection is accessed. Useful for testing.
	CollectionReporter CollectionReporterFn
}

// NewAnalyzingDistributor returns a new instance of AnalyzingDistributor.
func NewAnalyzingDistributor(s AnalyzingDistributorSettings) *AnalyzingDistributor {
	// collectionReport hook function defaults to no-op
	if s.CollectionReporter == nil {
		s.CollectionReporter = func(collection.Name) {}
	}

	return &AnalyzingDistributor{
		s:             s,
		lastSnapshots: make(map[string]*Snapshot),
	}
}

// Distribute implements snapshotter.Distributor
func (d *AnalyzingDistributor) Distribute(name string, s *Snapshot) {
	// Keep the most recent snapshot for each snapshot group we care about for analysis so we can combine them
	// For analysis, we want default and synthetic, and we can safely combine them since they are disjoint.
	if d.isAnalysisSnapshot(name) {
		d.snapshotsMu.Lock()
		d.lastSnapshots[name] = s
		d.snapshotsMu.Unlock()
	}

	// If the trigger snapshot is not set, simply bypass.
	// TODO(https://github.com/istio/istio/issues/17543): This should be replaced by a debounce logic
	if name != d.s.TriggerSnapshot {
		d.s.Distributor.Distribute(name, s)
		return
	}

	d.analysisMu.Lock()
	defer d.analysisMu.Unlock()

	// Cancel the previous analysis session, if it is still working.
	if d.cancelAnalysis != nil {
		close(d.cancelAnalysis)
		d.cancelAnalysis = nil
	}

	// start a new analysis session
	cancelAnalysis := make(chan struct{})
	d.cancelAnalysis = cancelAnalysis
	go d.analyzeAndDistribute(cancelAnalysis, name, s)
}

func (d *AnalyzingDistributor) isAnalysisSnapshot(s string) bool {
	for _, sn := range d.s.AnalysisSnapshots {
		if sn == s {
			return true
		}
	}

	return false
}

func (d *AnalyzingDistributor) analyzeAndDistribute(cancelCh chan struct{}, name string, s *Snapshot) {
	// For analysis, we use a combined snapshot
	ctx := &context{
		sn:                 d.getCombinedSnapshot(),
		cancelCh:           cancelCh,
		collectionReporter: d.s.CollectionReporter,
	}

	scope.Analysis.Debugf("Beginning analyzing the current snapshot")
	d.s.Analyzer.Analyze(ctx)
	scope.Analysis.Debugf("Finished analyzing the current snapshot, found messages: %v", ctx.messages)

	if !ctx.Canceled() {
		d.s.StatusUpdater.Update(ctx.messages)
	}

	// Execution only reaches this point for trigger snapshot group
	d.s.Distributor.Distribute(name, s)
}

// getCombinedSnapshot creates a new snapshot from the last snapshots of each snapshot group
// Important assumption: the collections in each snapshot don't overlap.
func (d *AnalyzingDistributor) getCombinedSnapshot() *Snapshot {
	var collections []*coll.Instance

	d.snapshotsMu.RLock()
	defer d.snapshotsMu.RUnlock()

	for _, s := range d.lastSnapshots {
		for _, n := range s.set.Names() {
			// Note that we don't clone the collections, so this combined snapshot is effectively a view into the
			// component snapshots.
			collections = append(collections, s.set.Collection(n))
		}
	}

	return &Snapshot{set: coll.NewSetFromCollections(collections)}
}

type context struct {
	sn                 *Snapshot
	cancelCh           chan struct{}
	messages           diag.Messages
	collectionReporter CollectionReporterFn
}

var _ analysis.Context = &context{}

// Report implements analysis.Context
func (c *context) Report(col collection.Name, m diag.Message) {
	c.messages.Add(m)
}

// Find implements analysis.Context
func (c *context) Find(col collection.Name, name resource.Name) *resource.Entry {
	c.collectionReporter(col)
	return c.sn.Find(col, name)
}

// Exists implements analysis.Context
func (c *context) Exists(col collection.Name, name resource.Name) bool {
	c.collectionReporter(col)
	return c.Find(col, name) != nil
}

// ForEach implements analysis.Context
func (c *context) ForEach(col collection.Name, fn analysis.IteratorFn) {
	c.collectionReporter(col)
	c.sn.ForEach(col, fn)
}

// Canceled implements analysis.Context
func (c *context) Canceled() bool {
	select {
	case <-c.cancelCh:
		return true
	default:
		return false
	}
}
