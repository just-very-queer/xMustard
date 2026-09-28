package injection

// Surface is where xMustard puts text into an agent's context.
type Surface string

const (
	// SurfaceRecall is memory the agent asked for: recall, a fetch by id, the memory
	// index.
	SurfaceRecall Surface = "recall"
	// SurfaceEvidence is reduced tool output: tool results and captured native output.
	SurfaceEvidence Surface = "evidence"
	// SurfaceHook is memory a hook pushes before a native tool call (WS-23): the memories
	// bound to a path being read or edited, or to a search pattern.
	SurfaceHook Surface = "hook"
	// SurfaceCore is memory pushed into every session unasked: the core-tier projection
	// in initialize.instructions and SessionStart (WS-31).
	SurfaceCore Surface = "core"
)

// Basis is how strongly a memory is verified, weakest first.
type Basis int

const (
	BasisUnverified Basis = iota
	BasisSelfAsserted
	BasisPeerVerified
	BasisHumanApproved
)

var basisNames = [...]string{"unverified", "self_asserted", "peer_verified", "human_approved"}

func (b Basis) String() string {
	if b < 0 || int(b) >= len(basisNames) {
		return basisNames[BasisUnverified]
	}
	return basisNames[b]
}

// modeBasis maps a promoted memory's verification_mode to its basis. A mode missing
// here (a newer build's, or none) is unverified.
var modeBasis = map[string]Basis{
	"peer_verified":           BasisPeerVerified,
	"single_agent":            BasisSelfAsserted,
	"self_asserted_open_mode": BasisSelfAsserted,
}

// BasisOf is the basis of a memory: unverified unless promoted; human_approved when a
// human approver approved the served revision; otherwise what its verification_mode
// says.
func BasisOf(promoted bool, verificationMode string, humanApproved bool) Basis {
	switch {
	case !promoted:
		return BasisUnverified
	case humanApproved:
		return BasisHumanApproved
	}
	return modeBasis[verificationMode]
}

// surfacePolicy is what a surface admits. A pulled surface serves what the caller's
// role may read, labeled; a pushed one reaches the agent unasked, so it admits only
// memory at minBasis or stronger that is neither quarantined nor flagged.
type surfacePolicy struct {
	push     bool
	minBasis Basis
}

// surfaces is the policy table. Core-tier and hook-injected memory need a human
// approver's approval.
var surfaces = map[Surface]surfacePolicy{
	SurfaceRecall:   {},
	SurfaceEvidence: {},
	SurfaceHook:     {push: true, minBasis: BasisHumanApproved},
	SurfaceCore:     {push: true, minBasis: BasisHumanApproved},
}

// Pushed reports whether s reaches the agent without being asked for.
func Pushed(s Surface) bool { return surfaces[s].push }

// Reasons a surface withholds text. Text below the surface's basis is withheld as
// "needs_<basis>": ReasonNeedsHumanApproval on the hook and core surfaces.
const (
	ReasonUnknownSurface     = "unknown_surface"
	ReasonQuarantined        = "quarantined"
	ReasonNeedsHumanApproval = "needs_human_approved"
	ReasonInstructionPattern = "instruction_pattern"
)

// Candidate is text a surface may carry, with what the policy decides on.
type Candidate struct {
	Basis Basis
	// Quarantine says why the text is quarantined; empty when it is not.
	Quarantine string
	Scan       Report
}

// Decision is the policy's answer for one candidate.
type Decision struct {
	Admit bool
	// Reason says why the candidate was withheld; empty when admitted.
	Reason string
	// Flags are the scan flags, reported either way so a pulled surface can label them.
	Flags []string
}

// Decide applies the surface's policy to c. The checks run in order and fail closed: an
// unknown surface admits nothing; a pulled surface admits the candidate with its flags;
// a pushed surface refuses quarantined text, then text below its basis, then flagged
// text (a truncated scan is a flag).
func Decide(s Surface, c Candidate) Decision {
	p, ok := surfaces[s]
	if !ok {
		return Decision{Reason: ReasonUnknownSurface, Flags: c.Scan.Flags}
	}
	d := Decision{Flags: c.Scan.Flags}
	switch {
	case !p.push:
		d.Admit = true
	case c.Quarantine != "":
		d.Reason = ReasonQuarantined
	case c.Basis < p.minBasis:
		d.Reason = "needs_" + p.minBasis.String()
	case !c.Scan.Clean():
		d.Reason = ReasonInstructionPattern
	default:
		d.Admit = true
	}
	return d
}
