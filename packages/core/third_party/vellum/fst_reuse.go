package vellum

// PROTOTYPE: state-reuse variants of AcceptWithVal/IsMatchWithVal. The stock
// methods call decoder.stateAt(addr, nil), which allocates a fresh fstStateV1 on
// every FST transition. fstStateV1 only references the FST data (no owned slices),
// so a single reusable instance can back the whole incremental walk, eliminating
// that per-transition allocation. Used by the fstcjk CJK segmenter.

// ReusableState is an opaque, reusable FST decode buffer. Hold one per walk (e.g.
// per Cut call) and pass it to the *State methods. NOT safe for concurrent use;
// give each goroutine its own.
type ReusableState struct {
	s fstStateV1
}

// NewReusableState returns a fresh reusable decode buffer.
func NewReusableState() *ReusableState { return &ReusableState{} }

// AcceptWithValState is AcceptWithVal but decodes into rs instead of allocating.
func (f *FST) AcceptWithValState(addr int, b byte, rs *ReusableState) (int, uint64) {
	s, err := f.decoder.stateAt(addr, &rs.s)
	if err != nil {
		return noneAddr, 0
	}
	_, next, output := s.TransitionFor(b)
	return next, output
}

// IsMatchWithValState is IsMatchWithVal but decodes into rs instead of allocating.
func (f *FST) IsMatchWithValState(addr int, rs *ReusableState) (bool, uint64) {
	s, err := f.decoder.stateAt(addr, &rs.s)
	if err != nil {
		return false, 0
	}
	return s.Final(), s.FinalOutput()
}
