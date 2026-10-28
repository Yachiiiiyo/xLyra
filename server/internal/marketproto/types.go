package marketproto

// Root is the payload of root.json. Time fields keep their original RFC3339 text.
type Root struct {
	V           int          `json:"v"`
	Seq         int64        `json:"seq"`
	IssuedAt    string       `json:"issuedAt"`
	OnlineKeys  []OnlineKey  `json:"onlineKeys"`
	RetiredKeys []RetiredKey `json:"retiredKeys"`
	Endpoints   Endpoints    `json:"endpoints"`
}

// OnlineKey is a hub signing key that is currently in service.
type OnlineKey struct {
	KeyID     string   `json:"keyId"`
	PublicKey string   `json:"publicKey"`
	Roles     []string `json:"roles"`
	NotBefore string   `json:"notBefore"`
	NotAfter  string   `json:"notAfter"`
}

// RetiredKey is a rotated hub key. It can only authenticate attestations
// whose issuedAt falls inside its validity window.
type RetiredKey struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
}

// Endpoints are the market URLs published in root.json.
// Hub and Push are omitted from the signature payload when null.
type Endpoints struct {
	Registry []string `json:"registry"`
	// 线上协议字段名仍为 hub,不可改
	Cloud    *string  `json:"hub"`
	Push     *string  `json:"push"`
}

// Index is the payload of index.json.
type Index struct {
	V           int      `json:"v"`
	Seq         int64    `json:"seq"`
	GeneratedAt string   `json:"generatedAt"`
	ExpiresAt   string   `json:"expiresAt"`
	Plugins     []Plugin `json:"plugins"`
}

// Plugin is one listing in the index.
type Plugin struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Summary   string          `json:"summary"`
	Kind      string          `json:"kind"`
	Developer Developer       `json:"developer"`
	Homepage  string          `json:"homepage,omitempty"`
	Icon      *Icon           `json:"icon,omitempty"`
	Versions  []PluginVersion `json:"versions"`
}

// Developer is the publisher of a plugin.
type Developer struct {
	Namespace    string   `json:"namespace"`
	DisplayName  string   `json:"displayName,omitempty"`
	Fingerprints []string `json:"fingerprints"`
}

// Icon points at an icon file served under the registry base URL.
type Icon struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// PluginVersion is one published build of a plugin.
type PluginVersion struct {
	Version       string   `json:"version"`
	PublishedAt   string   `json:"publishedAt"`
	FileSHA256    string   `json:"fileSha256"`
	PackageDigest string   `json:"packageDigest"`
	Size          int64    `json:"size"`
	Compat        Compat   `json:"compat"`
	Hosts         []string `json:"hosts"`
	Permissions   []string `json:"permissions"`
	Changelog     string   `json:"changelog"`
	Attestation   Envelope `json:"attestation"`
}

// Compat is the host requirement copied onto a plugin version.
type Compat struct {
	XLyra      string `json:"xlyra"`
	APIVersion int    `json:"apiVersion"`
	HostAPI    int    `json:"hostApi"`
}

// Attestation is the payload of an embedded publish attestation.
type Attestation struct {
	V                    int      `json:"v"`
	PluginID             string   `json:"pluginId"`
	Version              string   `json:"version"`
	PackageDigest        string   `json:"packageDigest"`
	DeveloperFingerprint string   `json:"developerFingerprint"`
	Kind                 string   `json:"kind"`
	Hosts                []string `json:"hosts"`
	Permissions          []string `json:"permissions"`
	IssuedAt             string   `json:"issuedAt"`
}

// Revocations is the payload of revocations.json.
type Revocations struct {
	V           int               `json:"v"`
	Seq         int64             `json:"seq"`
	GeneratedAt string            `json:"generatedAt"`
	ExpiresAt   string            `json:"expiresAt"`
	Entries     []RevocationEntry `json:"entries"`
}

// RevocationEntry is one revocation record.
type RevocationEntry struct {
	ID         string           `json:"id"`
	Target     RevocationTarget `json:"target"`
	Level      string           `json:"level"`
	Reason     string           `json:"reason"`
	At         string           `json:"at"`
	RollbackTo string           `json:"rollbackTo,omitempty"`
}

// RevocationTarget selects what an entry applies to.
type RevocationTarget struct {
	Type          string `json:"type"`
	PluginID      string `json:"pluginId,omitempty"`
	Version       string `json:"version,omitempty"`
	PackageDigest string `json:"packageDigest,omitempty"`
	Fingerprint   string `json:"fingerprint,omitempty"`
}

// KeyTransition is a developer key-rotation statement signed by the old key.
type KeyTransition struct {
	V              int    `json:"v"`
	Namespace      string `json:"namespace"`
	OldFingerprint string `json:"oldFingerprint"`
	NewPublicKey   string `json:"newPublicKey"`
	NewFingerprint string `json:"newFingerprint"`
	IssuedAt       string `json:"issuedAt"`
}
