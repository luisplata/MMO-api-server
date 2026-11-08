package content

// Canonical content loader (M1 alignment gate).
//
// data/canonical/content.json is the client-generated content snapshot:
// the 8 playable classes, their kits, the 40 ability definitions and the
// type-effectiveness table. This package only decodes and validates the
// artifact — it does not drive combat yet. The combat slice will consume
// the validated *Content; until then a boot-time failure here means the
// server refuses to start rather than serving a partial catalog.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

// ErrInvalidContent wraps every content validation failure so callers can
// distinguish a bad artifact from an unreadable file.
var ErrInvalidContent = errors.New("content: invalid canonical content")

// expectedPrimitives is the pinned, sorted engine primitive vocabulary.
// Every effects[].do must be declared here, and the artifact must declare
// exactly this set (order-insensitive).
var expectedPrimitives = []string{
	"aura",
	"beam",
	"blink",
	"buff",
	"cast",
	"cleanse",
	"cleanseSlow",
	"cone",
	"dash",
	"face",
	"floater",
	"heal",
	"mine",
	"projectile",
	"ring",
	"scatter",
	"shield",
	"spin",
	"thorns",
	"zoneAt",
	"zoneSelf",
}

// expectedTypes is the pinned, sorted type-effectiveness vocabulary.
var expectedTypes = []string{
	"Agua",
	"Electrico",
	"Fuego",
	"Planta",
	"Psiquico",
	"Siniestro",
	"Tierra",
	"Veneno",
}

const (
	// expectedClasses is the fixed roster size.
	expectedClasses = 8
	// abilityPerKit is the fixed number of ability ids per class.
	abilityPerKit = 3
	// minAbilities is the contract minimum (the artifact currently ships
	// exactly 40; the check also pins that count indirectly).
	minAbilities = 40
)

// Content mirrors the canonical content.json schema. Abilities is an
// object keyed by ability id, not an array.
type Content struct {
	ContentVersion string             `json:"contentVersion"`
	Meta           ContentMeta        `json:"meta"`
	Classes        []Class            `json:"classes"`
	Abilities      map[string]Ability `json:"abilities"`
	Types          TypeChart          `json:"types"`
}

// ContentMeta mirrors content.json's meta block.
type ContentMeta struct {
	SourceCommit string   `json:"sourceCommit"`
	GeneratedAt  string   `json:"generatedAt"`
	PxPerUnit    float64  `json:"pxPerUnit"`
	ClassOrder   []string `json:"classOrder"`
	Primitives   []string `json:"primitives"`
	HashRule     string   `json:"hashRule"`
}

// Class mirrors one playable class entry.
type Class struct {
	ClassID        int      `json:"classId"`
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	Type           string   `json:"type"`
	HP             int      `json:"hp"`
	Res            int      `json:"res"`
	ResRegen       int      `json:"resRegen"`
	SpeedPx        int      `json:"speedPx"`
	SpeedU         float64  `json:"speedU"`
	RadiusPx       int      `json:"radiusPx"`
	PreferredRange int      `json:"preferredRange"`
	PortraitKey    string   `json:"portraitKey"`
	Basic          *Basic   `json:"basic"`
	Kit            []string `json:"kit"`
}

// Basic mirrors the class basic attack. Pointer so a missing block is
// distinguishable from an empty one.
type Basic struct {
	Name       string  `json:"name"`
	Key        string  `json:"key"`
	Icon       string  `json:"icon"`
	CD         float64 `json:"cd"`
	Dmg        float64 `json:"dmg"`
	Range      float64 `json:"range"`
	Arc        float64 `json:"arc"`
	Kind       string  `json:"kind"`
	ProjSpeed  float64 `json:"projSpeed"`
	ProjRadius float64 `json:"projRadius"`
	RangeAI    float64 `json:"range_ai"`
	Color      string  `json:"color"`
	Slow       *Slow   `json:"slow"`
}

// Slow mirrors the optional slow payload shared by basics and effects.
type Slow struct {
	Pct float64 `json:"pct"`
	Dur float64 `json:"dur"`
}

// Ability mirrors one ability definition. PP is a pointer with a
// presence-aware contract: absent means unlimited uses, zero means no
// uses, and any decoded value must be >= 0.
type Ability struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Icon    string   `json:"icon"`
	CD      float64  `json:"cd"`
	Cost    float64  `json:"cost"`
	PP      *int     `json:"pp"`
	Types   []string `json:"types"`
	Range   float64  `json:"range"`
	AimMode string   `json:"aimMode"`
	Desc    string   `json:"desc"`
	Effects []Effect `json:"effects"`
}

// Effect carries the primitive discriminator. The remaining variant
// fields are client/runtime data and are intentionally not decoded here;
// validation only guarantees that every declared primitive exists.
type Effect struct {
	Do string `json:"do"`
}

// TypeChart mirrors the type-effectiveness table.
type TypeChart struct {
	Beats       map[string][]string `json:"beats"`
	Multipliers Multipliers         `json:"multipliers"`
}

// Multipliers pins the three damage multipliers.
type Multipliers struct {
	Strong  float64 `json:"strong"`
	Weak    float64 `json:"weak"`
	Neutral float64 `json:"neutral"`
}

// LoadContent reads and validates a canonical content.json from disk.
func LoadContent(path string) (*Content, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %w", ErrInvalidContent, path, err)
	}
	return ParseContent(data)
}

// ParseContent decodes and validates canonical content bytes. It is pure
// and never returns a partially validated Content.
func ParseContent(data []byte) (*Content, error) {
	var c Content
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidContent, err)
	}
	if err := validateContent(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

// validateContent runs every pinned content check in order and fails fast
// at the first violation.
func validateContent(c *Content) error {
	if c.ContentVersion == "" {
		return fmt.Errorf("%w: contentVersion is empty", ErrInvalidContent)
	}
	if c.Meta.SourceCommit == "" {
		return fmt.Errorf("%w: meta.sourceCommit is empty", ErrInvalidContent)
	}
	if err := validatePrimitives(c.Meta.Primitives); err != nil {
		return err
	}
	if err := validateClasses(c); err != nil {
		return err
	}
	primitives := make(map[string]struct{}, len(c.Meta.Primitives))
	for _, p := range c.Meta.Primitives {
		primitives[p] = struct{}{}
	}
	if err := validateAbilities(c.Abilities, primitives); err != nil {
		return err
	}
	if err := validateKits(c); err != nil {
		return err
	}
	return validateTypes(c.Types)
}

// validatePrimitives pins the declared primitive vocabulary: exactly the
// expected set, sorted, with no duplicates.
func validatePrimitives(declared []string) error {
	seen := make(map[string]struct{}, len(declared))
	for _, p := range declared {
		if p == "" {
			return fmt.Errorf("%w: meta.primitives contains an empty name", ErrInvalidContent)
		}
		if _, dup := seen[p]; dup {
			return fmt.Errorf("%w: meta.primitives duplicates %q", ErrInvalidContent, p)
		}
		seen[p] = struct{}{}
	}
	sorted := slices.Clone(declared)
	slices.Sort(sorted)
	if !slices.Equal(sorted, expectedPrimitives) {
		return fmt.Errorf("%w: meta.primitives = %v, want exactly %v", ErrInvalidContent, sorted, expectedPrimitives)
	}
	return nil
}

// validateClasses enforces the fixed roster, classId/classOrder agreement,
// unique keys and the per-class stat bounds.
func validateClasses(c *Content) error {
	if len(c.Meta.ClassOrder) != expectedClasses {
		return fmt.Errorf("%w: meta.classOrder has %d entries, want %d", ErrInvalidContent, len(c.Meta.ClassOrder), expectedClasses)
	}
	if len(c.Classes) != expectedClasses {
		return fmt.Errorf("%w: %d classes, want %d", ErrInvalidContent, len(c.Classes), expectedClasses)
	}
	seenIDs := make(map[int]struct{}, expectedClasses)
	seenKeys := make(map[string]struct{}, expectedClasses)
	for i := range c.Classes {
		class := &c.Classes[i]
		if class.ClassID < 1 || class.ClassID > expectedClasses {
			return fmt.Errorf("%w: classes[%d] classId %d outside 1..%d", ErrInvalidContent, i, class.ClassID, expectedClasses)
		}
		if _, dup := seenIDs[class.ClassID]; dup {
			return fmt.Errorf("%w: duplicate classId %d", ErrInvalidContent, class.ClassID)
		}
		seenIDs[class.ClassID] = struct{}{}
		if want := c.Meta.ClassOrder[class.ClassID-1]; class.Key != want {
			return fmt.Errorf("%w: classId %d key %q, want %q", ErrInvalidContent, class.ClassID, class.Key, want)
		}
		if _, dup := seenKeys[class.Key]; dup {
			return fmt.Errorf("%w: duplicate class key %q", ErrInvalidContent, class.Key)
		}
		seenKeys[class.Key] = struct{}{}
		if err := validateClassStats(class); err != nil {
			return err
		}
	}
	return nil
}

// validateClassStats checks the per-class resource and movement bounds.
func validateClassStats(class *Class) error {
	switch {
	case class.HP <= 0:
		return fmt.Errorf("%w: class %q hp %d must be > 0", ErrInvalidContent, class.Key, class.HP)
	case class.Res <= 0:
		return fmt.Errorf("%w: class %q res %d must be > 0", ErrInvalidContent, class.Key, class.Res)
	case class.ResRegen < 0:
		return fmt.Errorf("%w: class %q resRegen %d must be >= 0", ErrInvalidContent, class.Key, class.ResRegen)
	case class.SpeedU <= 0:
		return fmt.Errorf("%w: class %q speedU %v must be > 0", ErrInvalidContent, class.Key, class.SpeedU)
	case class.RadiusPx <= 0:
		return fmt.Errorf("%w: class %q radiusPx %d must be > 0", ErrInvalidContent, class.Key, class.RadiusPx)
	}
	return validateBasic(class.Key, class.Basic)
}

// validateBasic enforces the shared basic-attack contract and the
// kind-specific fields (melee: arc+range; proj: projSpeed+projRadius).
func validateBasic(classKey string, basic *Basic) error {
	if basic == nil {
		return fmt.Errorf("%w: class %q has no basic attack", ErrInvalidContent, classKey)
	}
	if basic.CD < 0 {
		return fmt.Errorf("%w: class %q basic cd %v must be >= 0", ErrInvalidContent, classKey, basic.CD)
	}
	if basic.Dmg < 0 {
		return fmt.Errorf("%w: class %q basic dmg %v must be >= 0", ErrInvalidContent, classKey, basic.Dmg)
	}
	switch basic.Kind {
	case "melee":
		if basic.Arc <= 0 || basic.Range <= 0 {
			return fmt.Errorf("%w: class %q melee basic needs arc > 0 and range > 0", ErrInvalidContent, classKey)
		}
	case "proj":
		if basic.ProjSpeed <= 0 || basic.ProjRadius <= 0 {
			return fmt.Errorf("%w: class %q proj basic needs projSpeed > 0 and projRadius > 0", ErrInvalidContent, classKey)
		}
	default:
		return fmt.Errorf("%w: class %q basic kind %q, want melee or proj", ErrInvalidContent, classKey, basic.Kind)
	}
	return nil
}

// validateAbilities checks every ability entry: map key matches its own
// id, cooldown is non-negative, pp (when present) is non-negative, effects
// are non-empty and only name pinned primitives.
func validateAbilities(abilities map[string]Ability, primitives map[string]struct{}) error {
	if len(abilities) < minAbilities {
		return fmt.Errorf("%w: %d abilities, want at least %d", ErrInvalidContent, len(abilities), minAbilities)
	}
	for id, ability := range abilities {
		if ability.ID != id {
			return fmt.Errorf("%w: abilities[%q] id %q does not match its key", ErrInvalidContent, id, ability.ID)
		}
		if ability.CD < 0 {
			return fmt.Errorf("%w: ability %q cd %v must be >= 0", ErrInvalidContent, id, ability.CD)
		}
		if ability.PP != nil && *ability.PP < 0 {
			return fmt.Errorf("%w: ability %q pp %d must be >= 0", ErrInvalidContent, id, *ability.PP)
		}
		if len(ability.Effects) == 0 {
			return fmt.Errorf("%w: ability %q has no effects", ErrInvalidContent, id)
		}
		for i, effect := range ability.Effects {
			if _, ok := primitives[effect.Do]; !ok {
				return fmt.Errorf("%w: ability %q effect[%d] uses unknown primitive %q", ErrInvalidContent, id, i, effect.Do)
			}
		}
	}
	return nil
}

// validateKits enforces three resolvable ability ids per class and the
// pinned total of 24 kit references.
func validateKits(c *Content) error {
	total := 0
	for i := range c.Classes {
		class := &c.Classes[i]
		if len(class.Kit) != abilityPerKit {
			return fmt.Errorf("%w: class %q kit has %d ids, want %d", ErrInvalidContent, class.Key, len(class.Kit), abilityPerKit)
		}
		for _, id := range class.Kit {
			if _, ok := c.Abilities[id]; !ok {
				return fmt.Errorf("%w: class %q kit references unknown ability %q", ErrInvalidContent, class.Key, id)
			}
		}
		total += len(class.Kit)
	}
	if total != expectedClasses*abilityPerKit {
		return fmt.Errorf("%w: %d total kit references, want %d", ErrInvalidContent, total, expectedClasses*abilityPerKit)
	}
	return nil
}

// validateTypes pins the type chart: all expected type keys present and
// the multiplier ordering strong > neutral > weak.
func validateTypes(chart TypeChart) error {
	if len(chart.Beats) != len(expectedTypes) {
		return fmt.Errorf("%w: types.beats has %d keys, want %d", ErrInvalidContent, len(chart.Beats), len(expectedTypes))
	}
	for _, name := range expectedTypes {
		if _, ok := chart.Beats[name]; !ok {
			return fmt.Errorf("%w: types.beats missing %q", ErrInvalidContent, name)
		}
	}
	m := chart.Multipliers
	if m.Strong <= 1 {
		return fmt.Errorf("%w: multipliers.strong %v must be > 1", ErrInvalidContent, m.Strong)
	}
	if m.Weak >= 1 {
		return fmt.Errorf("%w: multipliers.weak %v must be < 1", ErrInvalidContent, m.Weak)
	}
	if m.Neutral != 1 {
		return fmt.Errorf("%w: multipliers.neutral %v must be 1", ErrInvalidContent, m.Neutral)
	}
	return nil
}
