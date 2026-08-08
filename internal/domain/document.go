package domain

type DocType string

const (
	DocCreditAgreement    DocType = "credit_agreement"
	DocAmendment          DocType = "amendment"
	DocAuditReport        DocType = "audit_report"
	DocKYCDossier         DocType = "kyc_dossier"
	DocCorporateStructure DocType = "corporate_structure"
	DocFXTable            DocType = "fx_table"
	DocOther              DocType = "other"
)

type Document struct {
	ID         string `json:"doc_id"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Bytes      int64  `json:"bytes"`
	Pages      int    `json:"pages"`
	Chars      int    `json:"chars"`
	EmptyPages int    `json:"empty_pages"`
	NeedsOCR   bool   `json:"needs_ocr"`
	OCRUsed    bool   `json:"ocr_used"`
}

type Page struct {
	DocID string `json:"doc_id"`
	No    int    `json:"page"`
	Text  string `json:"text"`
}

type PageRef struct {
	DocID string `json:"doc_id"`
	Page  int    `json:"page"`
	Quote string `json:"quote"`
}
