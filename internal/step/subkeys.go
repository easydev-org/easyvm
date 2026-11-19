package step

// THE KEYS THAT ARE NOT STEPS. A SUBCKEY IS WRITTEN INSIDE ANOTHER KEY'S VALUE, AND A STEP THAT
// WRITES ONE AT TOP LEVEL IS A MISTAKE RATHER THAN A STEP WITH A STRANGE KEY.
//
// THE TWO USED TO BE ONE TABLE, and the consequence was a hole in the validation: `Keys` held `mean`,
// `words`, `mode`, `rect` and forty others beside `click`, `ssh` and `boot`, so
//
//   - see: {name: shot, rect: [...], of: [mean]}
//
// and
//
//   - mean: 40
//
// were both accepted, and the second is a step whose only key means nothing at all. The dispatcher
// then reports "no implementation for this step's keys", which is true and useless: the mistake was
// made two lines earlier, in the shape of the step.
//
// SO THE TABLES ARE SPLIT. `Keys` is the step vocabulary - what a step may say at its own level - and
// `Subkeys` is everything that belongs inside a value. `Validate` reads the first, and accepts a name
// from the second only when the step also names the key that reads it (see `OwnersOf`).
//
// KIND IS STILL DECLARED FOR EACH, because a subkey's shape is as checkable as a step key's and the
// owner hands its value to the same `checkKind` - `check:` does exactly that with `mode:` and
// `words:`.
var Subkeys = map[string]Key{
	// --- where a question is asked, and what it is compared with --------------------------------
	"rect": {Name: "rect", Kind: KindRect,
		Summary: "the region to measure, as a FRACTION of the frame (`middle`, `bottom 4.5%`, " +
			"`bottom 40px`) or `[x0, y0, x1, y1]` with `rect_at:`. A region belongs to the " +
			"QUESTION, so one captured frame can answer several"},
	"rect_at": {Name: "rect_at", Kind: KindPair,
		Summary: "with a PIXEL `rect:`: [width, height] the rectangle was measured at, so it can be " +
			"scaled for a guest drawing at another size"},
	"of": {Name: "of", Kind: KindList,
		Summary: "with the retired `see:`: which numbers to bind - mean, spread, sat, flat_rows, " +
			"contrast, min, max, or text. A question is a `check:` now"},
	"reference": {Name: "reference", Kind: KindString,
		Summary: "the image to compare against, relative to the walkthrough: with a `check:` of " +
			"`mode: like`, the region; with `desktop:`, the whole frame - which replaces six " +
			"brightness thresholds with one measurement"},
	"mode": {Name: "mode", Kind: KindString,
		Summary: "with `check:`: HOW the frame is questioned - like, mean_between, sat_between, " +
			"spread_between, text"},
	"words": {Name: "words", Kind: KindList,
		Summary: "with `check: mode: text`: the word-sets, any one of which satisfies it"},
	"without": {Name: "without", Kind: KindList,
		Summary: "with `check: mode: text`: words that must NOT be on screen"},
	"range": {Name: "range", Kind: KindList,
		Summary: "with a `check:` mode that compares a number: [low, high]"},
	"text":    {Name: "text", Kind: KindString, Summary: "the text a comparison is about"},
	"pattern": {Name: "pattern", Kind: KindString, Summary: "with `check: mode: matches`: the regular expression"},
	"as":      {Name: "as", Kind: KindString, Summary: "the name the boolean a gate or wait produces is bound under"},
	"from":    {Name: "from", Kind: KindString, Summary: "with `check:`: the frame a `see:` captured, by its name; with `until:`: the source (`console`)"},
	"must": {Name: "must", Kind: KindBool,
		Summary: "with `check:`: a FALSE answer fails the step. Without it a check only binds the " +
			"boolean, for an `if:` or a `loop: until:` to consume"},

	// --- the relations inside a `check:` --------------------------------------------------------
	"contains":     {Name: "contains", Kind: KindList, Summary: "a substring the reading must hold"},
	"contains_any": {Name: "contains_any", Kind: KindWordSets, Summary: "any one of these word-sets must appear"},
	"not_contains": {Name: "not_contains", Kind: KindList, Summary: "a substring the reading must not hold"},
	"equals":       {Name: "equals", Kind: KindString, Summary: "the reading equals this, normalised"},
	"matches":      {Name: "matches", Kind: KindString, Summary: "the reading matches this expression"},
	"like":         {Name: "like", Kind: KindString, Summary: "the reading is similar to this string"},
	"fuzzy":        {Name: "fuzzy", Kind: KindFloat, Summary: "how similar, for a fuzzy comparison"},
	"ratio":        {Name: "ratio", Kind: KindFloat, Summary: "the share of words that must match"},
	"mean":         {Name: "mean", Kind: KindFloat, Summary: "mean brightness, 0..255"},
	"spread":       {Name: "spread", Kind: KindFloat, Summary: "mean absolute deviation of brightness"},
	"sat":          {Name: "sat", Kind: KindFloat, Summary: "mean HSV saturation, 0..1"},
	"flat_rows":    {Name: "flat_rows", Kind: KindInt, Summary: "rows one colour across the full width"},
	"contrast":     {Name: "contrast", Kind: KindFloat, Summary: "how much the region varies"},
	"vs":           {Name: "vs", Kind: KindString, Summary: "the reading a comparison is made against"},
	"min":          {Name: "min", Kind: KindFloat, Summary: "the darkest pixel in the region"},
	"colour":       {Name: "colour", Kind: KindList, Summary: "a pixel criterion inside a check"},
	"any_of":       {Name: "any_of", Kind: KindWordSets, Summary: "any one of these values"},
	"less_than":    {Name: "less_than", Kind: KindFloat, Summary: "a comparison: below this"},
	"greater_than": {Name: "greater_than", Kind: KindFloat, Summary: "a comparison: above this"},
	"at_least":     {Name: "at_least", Kind: KindFloat, Summary: "a comparison: this or more"},
	"at_most":      {Name: "at_most", Kind: KindFloat, Summary: "a comparison: this or less"},
	"between":      {Name: "between", Kind: KindList, Summary: "a comparison: within this range"},

	// --- `loop:` ------------------------------------------------------------------------------
	"max": {Name: "max", Kind: KindInt,
		Summary: "with `loop:`: the attempt ceiling. Required - a loop with no ceiling presses a key " +
			"until the stage times out"},
	"every": {Name: "every", Kind: KindFloat,
		Summary: "with `loop:`: seconds after which the `clock:` actions run once, independent of the " +
			"reading"},
	"clock": {Name: "clock", Kind: KindList,
		Summary: "with `loop: every:`: the actions to run once when the clock runs out. It is named " +
			"`clock` because a bare YAML key spelled o-n parses as the boolean true"},
	"fact": {Name: "fact", Kind: KindString,
		Summary: "with `until:`: the boolean a loop ends on"},

	// --- `if:` ---------------------------------------------------------------------------------
	"then": {Name: "then", Kind: KindList, Summary: "with `if:`: the steps that run when it is true"},
	"else": {Name: "else", Kind: KindList, Summary: "with `if:`: the steps that run when it is false"},

	// --- keys whose arguments are written beside them rather than inside a mapping --------------
	"path": {Name: "path", Kind: KindString,
		Summary: "with `qga: file-read`/`file-write`: the file in the GUEST"},
	"to": {Name: "to", Kind: KindString,
		Summary: "with `scp: {template: KIND}`: the remote path it is copied to"},
}
