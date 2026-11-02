package marketproto

// Target is the local plugin a revocation list is matched against.
type Target struct {
	PluginID          string `json:"pluginId"`
	Version           string `json:"version"`
	PackageDigest     string `json:"packageDigest"`
	SignerFingerprint string `json:"signerFingerprint"`
	Attested          bool   `json:"attested"`
}

// Effect is one action produced by a matching revocation entry.
// Action is "disable" or "notify". RollbackTo is set only for a security
// revocation of a single package digest.
type Effect struct {
	EntryID    string `json:"entryId"`
	Action     string `json:"action"`
	RollbackTo string `json:"rollbackTo,omitempty"`
}

// MatchRevocations returns the effects that apply to t, in entry order.
//
//   - version matches packageDigest for both attested and manually uploaded
//     packages. security disables (and may carry rollbackTo); deprecated notifies
//     and ignores rollbackTo.
//   - plugin matches pluginId. Attested packages are disabled only at security
//     level; everything else notifies.
//   - developerKey matches the signer fingerprint. Attested packages are left
//     alone; manual uploads are notified.
func MatchRevocations(rev *Revocations, t Target) []Effect {
	out := []Effect{}
	if rev == nil {
		return out
	}
	for _, e := range rev.Entries {
		switch e.Target.Type {
		case "version":
			if t.PackageDigest == "" || e.Target.PackageDigest != t.PackageDigest {
				continue
			}
			switch e.Level {
			case "security":
				out = append(out, Effect{EntryID: e.ID, Action: "disable", RollbackTo: e.RollbackTo})
			case "deprecated":
				out = append(out, Effect{EntryID: e.ID, Action: "notify"})
			}
		case "plugin":
			if t.PluginID == "" || e.Target.PluginID != t.PluginID {
				continue
			}
			action := "notify"
			if t.Attested && e.Level == "security" {
				action = "disable"
			}
			out = append(out, Effect{EntryID: e.ID, Action: action})
		case "developerKey":
			if t.Attested || t.SignerFingerprint == "" || e.Target.Fingerprint != t.SignerFingerprint {
				continue
			}
			out = append(out, Effect{EntryID: e.ID, Action: "notify"})
		}
	}
	return out
}
