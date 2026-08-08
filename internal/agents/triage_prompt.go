package agents

import (
	"fmt"
	"strings"
)

const _triageInstruction = `You classify documents from a bank's mixed archive of corporate credit files.
The file names are opaque hashes and reveal nothing; only the content matters.

Return STRICT JSON with exactly these keys and no others:

{
  "doc_type": "credit_agreement | amendment | audit_report | kyc_dossier | corporate_structure | fx_table | other",
  "company_name": "the borrower or subject company this document is ABOUT, verbatim; empty string if none",
  "account_ids": ["ACC-1234"],
  "period": "the reporting or covenant period, e.g. FY2025 or 2025-01-01..2025-12-31; empty if none",
  "effective_date": "the date this document was signed or issued, YYYY-MM-DD; empty if unstated",
  "is_superseded": true/false,
  "amends_document": "what earlier document this one amends or restates; empty string if it does not",
  "counterparties_mentioned": ["other companies named in the document, excluding the bank itself"],
  "language": "ru | en | kk | mixed",
  "summary": "one sentence, max 25 words, describing what this document is",
  "confidence": 0.0
}

Rules:
- doc_type "credit_agreement" is only for the loan agreement itself (the contract that sets
  financial covenants). A document that changes an existing agreement is "amendment".
- Internal procedures, policies, incident reports, ops runbooks, HR documents, server logs and
  generic manuals are "other", even when they mention money or a client. A procedure that
  describes how KYC reviews are done is "other"; an actual dossier about one client is
  "kyc_dossier".
- "company_name" is the company the document is ABOUT, not the author. For a credit agreement
  that is the borrower, not the bank. For an audit report it is the audited company, not the
  audit firm.
- "is_superseded" is true only when the document itself says it has been replaced, is not in
  force, or is a prior revision.
- "counterparties_mentioned" lists at most 25 names; omit the lender and the auditor.
- "confidence" is your certainty about doc_type, from 0.0 to 1.0.
- Output JSON only. No prose, no markdown fence.`

func (in TriageInput) prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Document id: %s (%d pages)\n", in.DocID, in.Pages)
	if in.Findings != "" {
		fmt.Fprintf(&b, "\nDeterministic pre-scan (already extracted, trust it):\n%s\n", in.Findings)
	}
	if in.Text != "" {
		fmt.Fprintf(&b, "\n--- document text (may be truncated) ---\n%s\n--- end ---\n", in.Text)
	}
	if in.OCRText != "" {
		fmt.Fprintf(&b, "\nThis document has no text layer. Its pages were rendered and read by OCR;\n"+
			"the transcript below may contain recognition errors, so judge it by its shape and\n"+
			"vocabulary rather than by any single character.\n"+
			"--- OCR transcript ---\n%s\n--- end ---\n", in.OCRText)
	}
	return b.String()
}
