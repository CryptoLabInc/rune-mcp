// Package domain holds core types — DecisionRecord v2.1, 8 enums, 9 sub-models,
// and helpers.
package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// ─────────────────────────────────────────────────────────────────────────────
// Enums (8 total: 6 schema + 2 query — query enums live in query.go)
// ─────────────────────────────────────────────────────────────────────────────

// Domain — (19 values).
type Domain string

const (
	DomainArchitecture    Domain = "architecture"
	DomainSecurity        Domain = "security"
	DomainProduct         Domain = "product"
	DomainExec            Domain = "exec"
	DomainOps             Domain = "ops"
	DomainDesign          Domain = "design"
	DomainData            Domain = "data"
	DomainHR              Domain = "hr"
	DomainMarketing       Domain = "marketing"
	DomainIncident        Domain = "incident"
	DomainDebugging       Domain = "debugging"
	DomainQA              Domain = "qa"
	DomainLegal           Domain = "legal"
	DomainFinance         Domain = "finance"
	DomainSales           Domain = "sales"
	DomainCustomerSuccess Domain = "customer_success"
	DomainResearch        Domain = "research"
	DomainRisk            Domain = "risk"
	DomainGeneral         Domain = "general"
)

// ParseDomain — unknown → DomainGeneral (agent-delegated 관대함).
var domainList = []struct {
	Key string
	Val Domain
}{
	{"architecture", DomainArchitecture},
	{"security", DomainSecurity},
	{"product", DomainProduct},
	{"exec", DomainExec},
	{"ops", DomainOps},
	{"design", DomainDesign},
	{"data", DomainData},
	{"hr", DomainHR},
	{"marketing", DomainMarketing},
	{"incident", DomainIncident},
	{"debugging", DomainDebugging},
	{"qa", DomainQA},
	{"legal", DomainLegal},
	{"finance", DomainFinance},
	{"sales", DomainSales},
	{"customer_success", DomainCustomerSuccess},
	{"research", DomainResearch},
	{"risk", DomainRisk},
	{"general", DomainGeneral},
}

func ParseDomain(s string) Domain {
	if s == "" {
		return DomainGeneral
	}
	sLower := strings.ToLower(s)
	for _, entry := range domainList {
		if strings.Contains(sLower, entry.Key) {
			return entry.Val
		}
	}
	return DomainGeneral
}

// Sensitivity — (3 values). Default: SensitivityInternal.
type Sensitivity string

const (
	SensitivityPublic     Sensitivity = "public"
	SensitivityInternal   Sensitivity = "internal"
	SensitivityRestricted Sensitivity = "restricted"
)

// Status — (4 values). Default: StatusProposed.
// Recall rerank: STATUS_MULTIPLIER {accepted:1.0, proposed:0.9, superseded:0.5, reverted:0.3}.
type Status string

const (
	StatusProposed   Status = "proposed"
	StatusAccepted   Status = "accepted"
	StatusSuperseded Status = "superseded"
	StatusReverted   Status = "reverted"
)

// Certainty — (3 values). Default: CertaintyUnknown.
// INVARIANT: Supported requires evidence.quote; otherwise auto-downgrade.
type Certainty string

const (
	CertaintySupported          Certainty = "supported"
	CertaintyPartiallySupported Certainty = "partially_supported"
	CertaintyUnknown            Certainty = "unknown"
)

// ReviewState — (4 values). Default: ReviewStateUnreviewed.
type ReviewState string

const (
	ReviewStateUnreviewed ReviewState = "unreviewed"
	ReviewStateApproved   ReviewState = "approved"
	ReviewStateEdited     ReviewState = "edited"
	ReviewStateRejected   ReviewState = "rejected"
)

// SourceType — (7 values).
type SourceType string

const (
	SourceTypeSlack   SourceType = "slack"
	SourceTypeMeeting SourceType = "meeting"
	SourceTypeDoc     SourceType = "doc"
	SourceTypeGitHub  SourceType = "github"
	SourceTypeEmail   SourceType = "email"
	SourceTypeNotion  SourceType = "notion"
	SourceTypeOther   SourceType = "other"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sub-models (9 total)
// ─────────────────────────────────────────────────────────────────────────────

type SourceRef struct {
	Type    SourceType `json:"type"`
	URL     *string    `json:"url,omitempty"`
	Pointer *string    `json:"pointer,omitempty"`
}

type Evidence struct {
	Claim  string    `json:"claim"`
	Quote  string    `json:"quote"`
	Source SourceRef `json:"source"`
}

type Assumption struct {
	Assumption string  `json:"assumption"`
	Confidence float64 `json:"confidence"` // default 0.5
}

type Risk struct {
	Risk       string  `json:"risk"`
	Mitigation *string `json:"mitigation,omitempty"`
}

type DecisionDetail struct {
	What  string   `json:"what"`
	Who   []string `json:"who,omitempty"`
	Where string   `json:"where,omitempty"`
	When  string   `json:"when,omitempty"`
}

type Context struct {
	Problem      string       `json:"problem,omitempty"`
	Scope        *string      `json:"scope,omitempty"`
	Constraints  []string     `json:"constraints,omitempty"`
	Alternatives []string     `json:"alternatives,omitempty"`
	Chosen       string       `json:"chosen,omitempty"`
	TradeOffs    []string     `json:"trade_offs,omitempty"`
	Assumptions  []Assumption `json:"assumptions,omitempty"`
	Risks        []Risk       `json:"risks,omitempty"`
}

type Why struct {
	RationaleSummary string    `json:"rationale_summary,omitempty"`
	Certainty        Certainty `json:"certainty"` // default CertaintyUnknown
	MissingInfo      []string  `json:"missing_info,omitempty"`
}

type Quality struct {
	ScribeConfidence float64     `json:"scribe_confidence"` // default 0.5
	ReviewState      ReviewState `json:"review_state"`      // default Unreviewed
	ReviewedBy       *string     `json:"reviewed_by,omitempty"`
	ReviewNotes      *string     `json:"review_notes,omitempty"`
}

// Payload — Text is markdown (embedding fallback when reusable_insight empty).
type Payload struct {
	Format string `json:"format"` // fixed "markdown"
	Text   string `json:"text"`
}

// ─────────────────────────────────────────────────────────────────────────────
// DecisionRecord v2.1 — main schema
// ─────────────────────────────────────────────────────────────────────────────

// Console.Insert metadata's decrypted payload.
type DecisionRecord struct {
	SchemaVersion string `json:"schema_version"` // fixed "2.1"
	ID            string `json:"id"`
	Type          string `json:"type"` // fixed "decision_record"

	Domain       Domain      `json:"domain"`
	Sensitivity  Sensitivity `json:"sensitivity"`
	Status       Status      `json:"status"`
	SupersededBy *string     `json:"superseded_by,omitempty"`
	Timestamp    time.Time   `json:"timestamp"` // UTC, RFC3339

	Title    string         `json:"title"`
	Decision DecisionDetail `json:"decision"`
	Context  Context        `json:"context"`
	Why      Why            `json:"why"`
	Evidence []Evidence     `json:"evidence,omitempty"`

	Links []map[string]any `json:"links,omitempty"`
	Tags  []string         `json:"tags,omitempty"`

	GroupID    *string `json:"group_id,omitempty"`
	GroupType  *string `json:"group_type,omitempty"` // "phase_chain" | "bundle"
	PhaseSeq   *int    `json:"phase_seq,omitempty"`
	PhaseTotal *int    `json:"phase_total,omitempty"`

	OriginalText *string `json:"original_text,omitempty"`
	GroupSummary *string `json:"group_summary,omitempty"`

	ReusableInsight string `json:"reusable_insight"` // primary embedding target

	Quality Quality `json:"quality"`
	Payload Payload `json:"payload"`
}

// MaxTitleLen — (D3). UTF-8 rune-aware in Go.
const MaxTitleLen = 60

// MaxPhases — phase_chain max 7.
const MaxPhases = 7

// MaxBundleFacets — bundle max 5.
const MaxBundleFacets = 5

// ─────────────────────────────────────────────────────────────────────────────
// Helpers — GenerateRecordID / GenerateGroupID / EmbeddingTextForRecord
// ─────────────────────────────────────────────────────────────────────────────

// Word-level slug filter: first 3 lowercased words where each word (or word
// minus underscores) is fully alphanumeric. Non-ASCII letters/digits count
// as alphanumeric.
// Format: dec_{YYYY-MM-DD}_{domain}_{slug}.
func GenerateRecordID(ts time.Time, d Domain, title string) string {
	dateStr := ts.UTC().Format("2006-01-02")
	words := strings.Fields(strings.ToLower(title))
	if len(words) > 3 {
		words = words[:3]
	}
	kept := make([]string, 0, len(words))
	for _, w := range words {
		if isPyIsalnum(w) || isPyIsalnum(strings.ReplaceAll(w, "_", "")) {
			kept = append(kept, w)
		}
	}
	slug := strings.Join(kept, "_")
	return fmt.Sprintf("dec_%s_%s_%s", dateStr, string(d), slug)
}

// GenerateGroupID — same slug rule, "grp_" prefix.
func GenerateGroupID(ts time.Time, d Domain, title string) string {
	id := GenerateRecordID(ts, d, title)
	return "grp_" + strings.TrimPrefix(id, "dec_")
}

// - "" → false
// - all runes letter/digit → true
// - any punctuation/space/symbol → false
func isPyIsalnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// EmbeddingTextForRecord — reusable_insight (trimmed, non-empty) > payload.text.
func EmbeddingTextForRecord(r *DecisionRecord) string {
	if s := strings.TrimSpace(r.ReusableInsight); s != "" {
		return s
	}
	return r.Payload.Text
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation invariants
// ─────────────────────────────────────────────────────────────────────────────

// - No quote present → Supported auto-downgrades to Unknown
// - No evidence at all → Accepted auto-downgrades to Proposed
func EnsureEvidenceCertaintyConsistency(r *DecisionRecord) {
	hasQuotes := false
	for _, e := range r.Evidence {
		if e.Quote != "" {
			hasQuotes = true
			break
		}
	}
	if !hasQuotes && r.Why.Certainty == CertaintySupported {
		r.Why.Certainty = CertaintyUnknown
		const marker = "No direct quotes found in evidence"
		if !containsString(r.Why.MissingInfo, marker) {
			r.Why.MissingInfo = append(r.Why.MissingInfo, marker)
		}
	}
	if len(r.Evidence) == 0 && r.Status == StatusAccepted {
		r.Status = StatusProposed
	}
}

// ValidateEvidenceCertainty — (read-only). Returns false if invariant violated.
func ValidateEvidenceCertainty(r *DecisionRecord) bool {
	hasQuotes := false
	for _, e := range r.Evidence {
		if e.Quote != "" {
			hasQuotes = true
			break
		}
	}
	return !(r.Why.Certainty == CertaintySupported && !hasQuotes)
}

func containsString(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
