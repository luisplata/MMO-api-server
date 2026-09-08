// Package stats defines the base-attribute block shared by character
// templates and characters (design D6).
//
// Stats is pure data: the server never hardcodes these values. They are
// loaded from data/templates.json at runtime and snapshotted onto a
// character at creation, so a template rebalance never silently buffs or
// nerfs an existing character (spec "Stats nunca hardcodeadas").
package stats

// Stats is the base-attribute block of a character template.
//
// JSON field names are lowercase (hp/speed/atk/def) to match the proto
// Stats message and the JSON seed catalog, so one shape serves both the
// wire contract and the file repository.
type Stats struct {
	HP    float32 `json:"hp"`
	Speed float32 `json:"speed"`
	Atk   float32 `json:"atk"`
	Def   float32 `json:"def"`
}
