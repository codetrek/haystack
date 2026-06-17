package vectorstore

import "errors"

// AttrKind is the declared type of an indexed property. Keyword → equality/set
// (Eq/In); Numeric → ordered range (Range) plus equality.
type AttrKind uint8

const (
	Keyword AttrKind = 1
	Numeric AttrKind = 2
)

// ErrUnsupportedPredicate is returned for Tier-3 (OR/NOT/nested) predicates,
// which are 留口 (out of scope) in Phase 5.
var ErrUnsupportedPredicate = errors.New("vectorstore: unsupported predicate (OR/NOT/nested are not supported in Phase 5)")

// predKind identifies a leaf/composite predicate. The set is CLOSED in v1
// (Eq/In/Range/And); an extension point for OR/NOT exists via the interface but
// is intentionally unimplemented.
type predKind uint8

const (
	predEq predKind = iota
	predIn
	predRange
	predAnd
)

// Predicate is a filter AST node. Construct via Eq/In/Range/And. A nil Predicate
// means "no filter" (the unfiltered path). The concrete type is unexported so the
// set stays closed; callers cannot synthesize an OR/NOT node.
type Predicate interface {
	kind() predKind
	evalPayload(p Payload) bool // independent brute evaluator (oracle + residual scan)
	props() []string            // declared properties this predicate touches
}

type eqPred struct {
	prop string
	val  Value
}
type inPred struct {
	prop string
	vals []Value
}
type rangePred struct {
	prop   string
	lo, hi Value // inclusive [lo, hi]; both must be the same numeric kind
}
type andPred struct{ preds []Predicate }

func Eq(prop string, v Value) Predicate         { return eqPred{prop, v} }
func In(prop string, vs ...Value) Predicate     { return inPred{prop, vs} }
func Range(prop string, lo, hi Value) Predicate { return rangePred{prop, lo, hi} }
func And(preds ...Predicate) Predicate          { return andPred{preds} }

func (eqPred) kind() predKind    { return predEq }
func (inPred) kind() predKind    { return predIn }
func (rangePred) kind() predKind { return predRange }
func (andPred) kind() predKind   { return predAnd }

func (p eqPred) evalPayload(pl Payload) bool {
	v, ok := pl[p.prop]
	return ok && v.equal(p.val)
}
func (p inPred) evalPayload(pl Payload) bool {
	v, ok := pl[p.prop]
	if !ok {
		return false
	}
	for _, c := range p.vals {
		if v.equal(c) {
			return true
		}
	}
	return false
}
func (p rangePred) evalPayload(pl Payload) bool {
	v, ok := pl[p.prop]
	if !ok {
		return false
	}
	x, okx := v.numeric()
	lo, okl := p.lo.numeric()
	hi, okh := p.hi.numeric()
	if !okx || !okl || !okh {
		return false
	}
	return x >= lo && x <= hi
}
func (p andPred) evalPayload(pl Payload) bool {
	for _, c := range p.preds {
		if !c.evalPayload(pl) {
			return false
		}
	}
	return true
}

func (p eqPred) props() []string    { return []string{p.prop} }
func (p inPred) props() []string    { return []string{p.prop} }
func (p rangePred) props() []string { return []string{p.prop} }
func (p andPred) props() []string {
	var out []string
	for _, c := range p.preds {
		out = append(out, c.props()...)
	}
	return out
}

// validatePredicate rejects kind mismatches (Range on a Keyword field) and any
// node outside the closed set. A nil predicate is allowed (no filter). decls maps
// declared property → kind; a property not in decls is fine (residual scan).
func validatePredicate(p Predicate, decls map[string]AttrKind) error {
	if p == nil {
		return nil
	}
	switch t := p.(type) {
	case eqPred, inPred:
		return nil
	case rangePred:
		if k, ok := decls[t.prop]; ok && k == Keyword {
			return errors.New("vectorstore: Range predicate on Keyword field " + t.prop)
		}
		return nil
	case andPred:
		for _, c := range t.preds {
			if err := validatePredicate(c, decls); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrUnsupportedPredicate
	}
}
