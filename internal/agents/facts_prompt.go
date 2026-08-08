package agents

import (
	"fmt"
	"strings"
)

const _factsInstruction = `You read a borrower's audit file and compliance dossier and extract, exactly, the
disclosures that change what a loan covenant sees.

You do NOT compute totals and you do NOT decide compliance. You transcribe disclosures.

Return STRICT JSON:

{
  "adjustments": [
    {
      "kind": "reclassify | exclude_period | include_period | disclosed_amount | ledger_amount_fix | ebitda_add_back | no_change",
      "txn_id": "the transaction id the disclosure names, in the TXN-<scenario>-NNNN form given above; empty if it names none",
      "counterparty": "the counterparty named, else empty",
      "amount": "decimal string, no currency sign or separators; empty if the disclosure states none",
      "from_category": "the category the amount was originally booked to, else empty",
      "to_category": "the category it is moved to, else empty",
      "rationale": "the auditor's stated reason, briefly",
      "applied": true|false,
      "source_doc": "the document id this came from",
      "quote": "the sentence, verbatim"
    }
  ],
  "parties": [
    {"name": "...", "voting_share": "44.7", "pledged_share": "63.9 if a collateral table gives this entity's pledged asset share, else empty",
     "relation": "affiliate|subsidiary|parent|", "status": "restricted|unrestricted| — only if stated in words", "source_doc": "...", "quote": "..."}
  ],
  "related_party_threshold": "the voting share percentage at or above which a counterparty counts as related, formatted like 47.5; empty if the file states none",
  "unrestricted_threshold": "the pledged-asset percentage BELOW which a subsidiary is outside the security and counts as unrestricted, formatted like 62.5; empty if the file states none",
  "fx_rates": [{"currency": "EUR", "usd_rate": "1.0725", "basis": "how the rate was established", "quote": "..."}],
  "notes": ["anything material you saw that does not fit above"],
  "confidence": 0.0
}

Rules:

1. APPLIED vs REJECTED. "applied" is false when the auditor says the question was considered
   and the original classification is retained, or that no adjustment is required, or that the
   matter was reviewed and no change was made. Record those with kind "no_change" — they exist
   so a later stage can prove they were NOT applied. Everything the auditor actually concluded
   gets applied true.

2. ONLY FINAL POSITIONS. If a document says it is a draft, an interim worksheet, a preliminary
   position, or that it is replaced by a final report, IGNORE its conclusions entirely. Do not
   emit adjustments from it. Say so in "notes".

3. AN EMPTY NOTE MEANS ZERO, NOT UNKNOWN. If a heading such as "EBITDA adjustments" appears
   with no items under it, emit no adjustments for it and add a note saying the section was
   present and empty. Never invent an item to fill it.

4. THRESHOLD. The compliance dossier states the voting share at or above which a counterparty
   is a related party, in words of the form "организации, в которых Группа владеет N% и более
   голосующих прав, признаются связанными сторонами". N differs between borrowers and is never
   a round default. Copy the number printed in THIS borrower's file; if the file states none,
   leave the field empty rather than supplying a customary figure.

5. PARTIES. Transcribe every organisation in the ownership table with its voting share, whether
   or not it meets the threshold — a share below the threshold is evidence too. Do not decide
   relatedness yourself; just report the shares.

5a. COLLATERAL COVERAGE. A dossier may carry a SECOND table — a heading about the security or
   collateral coverage of subsidiaries — giving the share of each subsidiary's assets pledged
   under the security agreement, plus the percentage below which a subsidiary sits outside the
   security perimeter and is therefore "unrestricted". Such a table often names entities the
   ownership table does not, and often has no text layer. Transcribe those entities too, with
   "pledged_share" set and "voting_share" empty, and put the percentage in
   "unrestricted_threshold". Do not decide restricted status yourself.

6. AMOUNTS. Transcribe exactly as written, dropping the currency sign and the separators:
   "$9,876,543.21" becomes "9876543.21". That is an invented example of the FORMAT; every
   amount you return must be one the document prints.

6a. MISSING LEDGER AMOUNTS. The prompt names the rows whose amount the ledger export left blank.
   If ANY document in this file states the amount of such a row — a treasury memo, an internal
   note, a footnote — emit it as "ledger_amount_fix" with applied true and the txn_id. This is
   not a reclassification: an audit report saying that no reclassifications were required says
   nothing about a missing figure, and leaving it out reports the row as zero.

6b. FIGURES THAT HAVE NO ROW AT ALL. A note may state an amount the ledger deliberately does
   not carry as a transaction — an obligation, a provision, an accrued or aggregate liability
   disclosed so that a covenant can be measured over it. Wording of the shape "раскрывается и
   не отражается отдельной операцией", "disclosed and not recorded as a separate entry", "для
   целей агрегирования по ковенантам" marks exactly this. Emit it as "disclosed_amount" with
   applied true and no txn_id, because no row exists to name.

   Do not confuse it with 6a: there the row exists and only its amount is blank in the export.
   Here there is no row and there never was one. A covenant that aggregates such a line is
   reported short by the whole amount when the note is left out, and nothing downstream can
   notice the omission.

7. OCR PAGES. Some pages have no text layer and were transcribed by OCR; the ownership table is
   often one of them. The transcript can be noisy: read it for its structure, and prefer a figure
   the surrounding text confirms over a lone garbled one.

Output JSON only. No prose, no markdown fence.`

const _groupPPEInstruction = `
Add one more key to your JSON, "group_ppe", built from the CONSOLIDATED statements above:

{
  "parent": "the parent company the statements belong to",
  "period": "the year they cover, formatted like 2019",
  "opening": "net book value of property, plant and equipment at the START of the year",
  "closing": "net book value at the END of the year",
  "depreciation": "the depreciation charge for the year",
  "disposals": "net book value of disposals during the year; \"0\" if the note says there were none",
  "disposals_stated": true|false,
  "source_doc": "the document id",
  "quote": "the sentence about disposals, verbatim"
}

Transcribe those figures and nothing else. Do NOT add, subtract or infer capital expenditure:
that arithmetic is done outside, and only when disposals are pinned down.
"disposals_stated" is true only when the note actually says what disposals were, including when
it says there were none. If the note is silent about them, set it false and leave "disposals"
empty — a silent note is not a note saying zero.`

func (in FactsInput) prompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Borrower: %s (scenario %s)\n", in.Company, in.ScenarioID)
	fmt.Fprintf(&b, "Its transactions are identified as TXN-%s-NNNN.\n", in.ScenarioID)
	if len(in.MissingAmounts) > 0 {
		fmt.Fprintf(&b, "The ledger export left the amount of these rows BLANK: %s.\n"+
			"If any document below states what one of them was, that is a ledger_amount_fix.\n",
			strings.Join(in.MissingAmounts, ", "))
	}
	for _, d := range in.Documents {
		fmt.Fprintf(&b, "\n═══ document %s [%s] ═══\n%s\n", d.DocID, d.DocType, d.Text)
		if len(d.OCRPages) > 0 {
			fmt.Fprintf(&b, "[pages %v of this document have no text layer; OCR transcript follows]\n%s\n",
				d.OCRPages, d.OCRText)
		}
	}
	if g := in.GroupDoc; g != nil {
		fmt.Fprintf(&b, "\n═══ CONSOLIDATED STATEMENTS OF THE PARENT — document %s ═══\n"+
			"These cover the whole group, not this borrower alone. Read them only for the group\n"+
			"figures asked for below; they say nothing about this borrower's own adjustments or\n"+
			"ownership.\n%s\n%s\n", g.DocID, g.Text, _groupPPEInstruction)
	}
	return b.String()
}
