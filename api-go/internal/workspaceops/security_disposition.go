package workspaceops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Security review depth (FRONTIER Lane 4): an analyst-disposition overlay on top
// of vulnerability findings — disposition, exploitability, suppression,
// risk-acceptance, and verification linkage — kept separate from the immutable
// finding record so triage state is auditable without mutating scan output.

type SecurityDisposition struct {
	FindingID                  string   `json:"finding_id"`
	WorkspaceID                string   `json:"workspace_id"`
	Disposition                string   `json:"disposition"`   // open|confirmed|false_positive|mitigated|accepted_risk|suppressed
	Exploitability             string   `json:"exploitability"` // unknown|none|theoretical|poc|exploited
	Suppressed                 bool     `json:"suppressed"`
	SuppressionReason          string   `json:"suppression_reason,omitempty"`
	RiskAcceptedBy             string   `json:"risk_accepted_by,omitempty"`
	RiskAcceptanceNote         string   `json:"risk_acceptance_note,omitempty"`
	LinkedVerificationProfiles []string `json:"linked_verification_profile_ids"`
	Reviewer                   string   `json:"reviewer,omitempty"`
	Notes                      string   `json:"notes,omitempty"`
	UpdatedAt                  string   `json:"updated_at"`
}

var validDispositions = map[string]struct{}{
	"open": {}, "confirmed": {}, "false_positive": {}, "mitigated": {}, "accepted_risk": {}, "suppressed": {},
}
var validExploitability = map[string]struct{}{
	"unknown": {}, "none": {}, "theoretical": {}, "poc": {}, "exploited": {},
}

func securityDispositionsPath(dataDir, workspaceID string) string {
	return filepath.Join(dataDir, "workspaces", workspaceID, "security_dispositions.json")
}

func loadSecurityDispositions(dataDir, workspaceID string) ([]SecurityDisposition, error) {
	var dispositions []SecurityDisposition
	if err := readJSON(securityDispositionsPath(dataDir, workspaceID), &dispositions); err != nil {
		if os.IsNotExist(err) {
			return []SecurityDisposition{}, nil
		}
		return nil, err
	}
	return dispositions, nil
}

func defaultDisposition(workspaceID, findingID string) SecurityDisposition {
	return SecurityDisposition{
		FindingID:                  findingID,
		WorkspaceID:                workspaceID,
		Disposition:                "open",
		Exploitability:             "unknown",
		LinkedVerificationProfiles: []string{},
	}
}

// ListSecurityDispositions returns all dispositions for a workspace.
func ListSecurityDispositions(dataDir, workspaceID string) ([]SecurityDisposition, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	return loadSecurityDispositions(dataDir, workspaceID)
}

// GetSecurityDisposition returns one disposition (default if not yet set).
func GetSecurityDisposition(dataDir, workspaceID, findingID string) (*SecurityDisposition, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	dispositions, err := loadSecurityDispositions(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, d := range dispositions {
		if d.FindingID == findingID {
			return &d, nil
		}
	}
	def := defaultDisposition(workspaceID, findingID)
	return &def, nil
}

// SetSecurityDisposition validates and upserts a disposition, recording an audit event.
func SetSecurityDisposition(dataDir, workspaceID, findingID string, in SecurityDisposition) (*SecurityDisposition, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	in.FindingID = findingID
	in.WorkspaceID = workspaceID
	in.Disposition = strings.ToLower(strings.TrimSpace(in.Disposition))
	if in.Disposition == "" {
		in.Disposition = "open"
	}
	if _, ok := validDispositions[in.Disposition]; !ok {
		return nil, fmt.Errorf("invalid disposition %q", in.Disposition)
	}
	in.Exploitability = strings.ToLower(strings.TrimSpace(in.Exploitability))
	if in.Exploitability == "" {
		in.Exploitability = "unknown"
	}
	if _, ok := validExploitability[in.Exploitability]; !ok {
		return nil, fmt.Errorf("invalid exploitability %q", in.Exploitability)
	}
	if in.Disposition == "accepted_risk" && strings.TrimSpace(in.RiskAcceptanceNote) == "" {
		return nil, fmt.Errorf("accepted_risk requires risk_acceptance_note")
	}
	if in.Suppressed && strings.TrimSpace(in.SuppressionReason) == "" {
		return nil, fmt.Errorf("suppressed findings require suppression_reason")
	}
	if in.LinkedVerificationProfiles == nil {
		in.LinkedVerificationProfiles = []string{}
	}
	in.LinkedVerificationProfiles = cleanStrings(in.LinkedVerificationProfiles)
	in.UpdatedAt = nowUTC()

	dispositions, err := loadSecurityDispositions(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i, d := range dispositions {
		if d.FindingID == findingID {
			dispositions[i] = in
			replaced = true
			break
		}
	}
	if !replaced {
		dispositions = append(dispositions, in)
	}
	if err := writeJSON(securityDispositionsPath(dataDir, workspaceID), dispositions); err != nil {
		return nil, err
	}
	_ = recordAuditEventNoGuard(dataDir, workspaceID, AuditEvent{
		Actor:      in.Reviewer,
		Action:     "security.disposition.update",
		TargetType: "vulnerability_finding",
		TargetID:   findingID,
		Details:    fmt.Sprintf("disposition=%s exploitability=%s suppressed=%t", in.Disposition, in.Exploitability, in.Suppressed),
	})
	return &in, nil
}

// SecurityReviewPacket aggregates disposition state for an exportable review.
type SecurityReviewPacket struct {
	WorkspaceID         string         `json:"workspace_id"`
	TotalDispositioned  int            `json:"total_dispositioned"`
	ByDisposition       map[string]int `json:"by_disposition"`
	ByExploitability    map[string]int `json:"by_exploitability"`
	OpenExploitable     int            `json:"open_exploitable"`
	SuppressedCount     int            `json:"suppressed_count"`
	AcceptedRiskCount   int            `json:"accepted_risk_count"`
	UnverifiedConfirmed int            `json:"unverified_confirmed"`
	GeneratedAt         string         `json:"generated_at"`
}

func summarizeSecurityReview(workspaceID string, dispositions []SecurityDisposition) SecurityReviewPacket {
	packet := SecurityReviewPacket{
		WorkspaceID:      workspaceID,
		ByDisposition:    map[string]int{},
		ByExploitability: map[string]int{},
		GeneratedAt:      nowUTC(),
	}
	packet.TotalDispositioned = len(dispositions)
	for _, d := range dispositions {
		packet.ByDisposition[d.Disposition]++
		packet.ByExploitability[d.Exploitability]++
		if d.Suppressed {
			packet.SuppressedCount++
		}
		if d.Disposition == "accepted_risk" {
			packet.AcceptedRiskCount++
		}
		if (d.Disposition == "open" || d.Disposition == "confirmed") &&
			(d.Exploitability == "poc" || d.Exploitability == "exploited") {
			packet.OpenExploitable++
		}
		if d.Disposition == "confirmed" && len(d.LinkedVerificationProfiles) == 0 {
			packet.UnverifiedConfirmed++
		}
	}
	return packet
}

// BuildSecurityReviewPacket builds the workspace security review summary.
func BuildSecurityReviewPacket(dataDir, workspaceID string) (*SecurityReviewPacket, error) {
	if _, err := loadSnapshot(dataDir, workspaceID); err != nil {
		return nil, err
	}
	dispositions, err := loadSecurityDispositions(dataDir, workspaceID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(dispositions, func(i, j int) bool {
		return dispositions[i].FindingID < dispositions[j].FindingID
	})
	packet := summarizeSecurityReview(workspaceID, dispositions)
	return &packet, nil
}
