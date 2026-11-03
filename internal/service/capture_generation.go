package service

import (
	"io"
	"slices"

	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

// A generation owns its resources until it is unpublished AND every capture
// using it has returned. Publication and lease acquisition share generationMu,
// so a reader cannot acquire an already-drained generation.
type captureGeneration struct {
	dependencies CaptureDependencies
	users        int
	retired      bool
}

type captureResource struct {
	generations int
	retire      func()
}

func (s *CaptureService) SetDependencies(d CaptureDependencies) {
	d.AgentDEK = slices.Clone(d.AgentDEK)
	d.Capabilities = slices.Clone(d.Capabilities)
	s.generationMu.Lock()
	if s.stopped {
		s.generationMu.Unlock()
		lifecycle.ZeroizeDEK(d.AgentDEK)
		return
	}
	next := &captureGeneration{dependencies: d}
	if s.resources == nil {
		s.resources = make(map[io.Closer]*captureResource)
	}
	// Take the new ownership before dropping the old: unchanged/shared handles
	// must remain open across publication, including several overlapping captures.
	for _, resource := range generationResources(&d) {
		entry := s.resources[resource]
		if entry == nil {
			entry = &captureResource{}
			s.resources[resource] = entry
		}
		entry.generations++
	}
	old := s.dependencies.Swap(next)
	var callbacks []func()
	if old != nil {
		old.retired = true
		callbacks = s.drainGenerationLocked(old)
	}
	s.generationMu.Unlock()
	runRetirements(callbacks)
}

// Dependencies returns a detached snapshot for diagnostics and inspection.
func (s *CaptureService) Dependencies() CaptureDependencies {
	s.generationMu.Lock()
	defer s.generationMu.Unlock()
	if generation := s.dependencies.Load(); generation != nil {
		d := generation.dependencies
		d.AgentDEK = slices.Clone(d.AgentDEK)
		d.Capabilities = slices.Clone(d.Capabilities)
		return d
	}
	return CaptureDependencies{}
}

func (s *CaptureService) acquireGeneration() (*CaptureDependencies, func()) {
	s.generationMu.Lock()
	generation := s.dependencies.Load()
	if generation == nil {
		s.generationMu.Unlock()
		return &CaptureDependencies{}, func() {}
	}
	generation.users++
	s.generationMu.Unlock()
	return &generation.dependencies, func() {
		s.generationMu.Lock()
		generation.users--
		callbacks := s.drainGenerationLocked(generation)
		s.generationMu.Unlock()
		runRetirements(callbacks)
	}
}

// AfterCaptureDrain defers adapter retirement until every generation that owns
// it is unpublished and drained. It also protects the gap between Inject* and
// ApplyConsoleBundle, when the old generation is still the published one.
func (s *CaptureService) AfterCaptureDrain(resource io.Closer, retire func()) {
	s.generationMu.Lock()
	if entry := s.resources[resource]; entry != nil {
		// Multiple boot attempts may replace/reuse one still-live resource. It has
		// one close action, even if it participates in several generations.
		if entry.retire == nil {
			entry.retire = retire
		}
		s.generationMu.Unlock()
		return
	}
	s.generationMu.Unlock()
	retire()
}

// Shutdown prevents future acquisitions/publications. Existing captures keep
// their leases; resources and DEKs are released only when those calls return,
// even if the outer process shutdown deadline expires first.
func (s *CaptureService) Shutdown() {
	s.generationMu.Lock()
	s.stopped = true
	current := s.dependencies.Swap(nil)
	var callbacks []func()
	if current != nil {
		current.retired = true
		callbacks = s.drainGenerationLocked(current)
	}
	s.generationMu.Unlock()
	runRetirements(callbacks)
}

func (s *CaptureService) drainGenerationLocked(g *captureGeneration) []func() {
	if !g.retired || g.users != 0 {
		return nil
	}
	// No future caller can acquire this generation and no existing caller uses
	// its metadata key. Clear this owned key copy before releasing resources.
	lifecycle.ZeroizeDEK(g.dependencies.AgentDEK)
	var callbacks []func()
	for _, resource := range generationResources(&g.dependencies) {
		entry := s.resources[resource]
		entry.generations--
		if entry.generations == 0 {
			delete(s.resources, resource)
			if entry.retire != nil {
				callbacks = append(callbacks, entry.retire)
			}
		}
	}
	return callbacks
}

func generationResources(d *CaptureDependencies) []io.Closer {
	resources := []io.Closer{}
	seen := map[io.Closer]bool{}
	add := func(resource io.Closer) {
		if resource != nil && !seen[resource] {
			seen[resource] = true
			resources = append(resources, resource)
		}
	}
	add(d.Console)
	add(d.Embedder)
	if closer, ok := d.Encryptor.(io.Closer); ok {
		add(closer)
	}
	return resources
}

func runRetirements(callbacks []func()) {
	for _, retire := range callbacks {
		retire()
	}
}
