package models

import (
	"sort"
	"strings"
)

// Route is one way to reach a model: a provider ultra-zen can actually talk
// to, plus the model as that provider lists it. The same model reached two
// ways is two routes — that duplication is the point, because the two differ
// in price, context window and availability.
type Route struct {
	Provider string // ultra-zen provider name: "openrouter", "opencode-go", "groq", ...
	Model    Model
}

// Group is one matched model together with every route that serves it.
//
// A model is not a row, it is a set of routes: GLM 5.2 might be reachable via
// OpenRouter (priced, and itself fanning out to a dozen upstreams), via the
// Zen go tier (billed against credits, no published rate) and as a free
// variant. Presenting those as three unrelated search hits hides the only
// question worth asking — which of these should I use — so they are merged
// under one identity and ranked against each other.
type Group struct {
	Key    string  // normalised identity the routes were merged on
	Title  string  // display name, taken from the best-scoring route
	Routes []Route // free first, then cheapest published price
	Score  int     // best match score across the routes
}

// Cheapest returns the group's lowest published price, if any route has one.
func (g Group) Cheapest() (Price, bool) {
	var best Price
	for _, r := range g.Routes {
		if !r.Model.Price.Known {
			continue
		}
		if !best.Known || r.Model.Price.PromptUSD < best.PromptUSD {
			best = r.Model.Price
		}
	}
	return best, best.Known
}

// HasFree reports whether any route to this model costs nothing — either a
// published zero price or a provider tier ultra-zen knows to be free.
func (g Group) HasFree() bool {
	for _, r := range g.Routes {
		if r.Model.Free || r.Model.Price.Free() {
			return true
		}
	}
	return false
}

// fold reduces a string to its comparable core: lowercase, letters and digits
// only. Model ids are written half a dozen ways for the same weights —
// "z-ai/glm-5.2", "glm-5.2", "GLM 5.2", "glm_5_2" — and a user typing a query
// has no reason to reproduce any one of them exactly. Folding both sides means
// separators never decide a match.
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// identity is the key models are merged on across providers. It drops the
// vendor prefix ("z-ai/glm-5.2" and the Zen gateway's bare "glm-5.2" are the
// same model) and the free-tier marker (OpenRouter's ":free" suffix, Zen's
// "-free") so a model's free and paid routes land in one group instead of
// two adjacent near-duplicates.
func identity(id string) string {
	s := strings.TrimSpace(strings.ToLower(id))
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "-free")
	return fold(s)
}

// Match scores how well query matches a model's id and name. Higher is a
// better match; -1 means no match at all. An empty query matches everything
// at score 0, so a blank search box lists the catalog rather than nothing.
//
// The tiers, strongest first: exact id, exact id ignoring the vendor prefix,
// a prefix of the id, a substring of the id, a substring of the friendly
// name, then a subsequence of either. Subsequence matching is what lets
// "dsv4" find "deepseek/deepseek-v4-flash"; it scores lowest because it also
// matches a lot of noise.
func Match(query, id, name string) int {
	q := fold(query)
	if q == "" {
		return 0
	}
	fid, fname := fold(id), fold(name)
	bare := identity(id)

	// Shorter haystacks are tighter matches: "glm5" against "glm-5" should
	// beat "glm5" against "glm-5.2-thinking-preview". The bonus is capped so
	// it can never promote a weaker tier above a stronger one.
	tightness := func(hay string) int {
		if len(hay) <= len(q) {
			return 40
		}
		if d := 40 - (len(hay) - len(q)); d > 0 {
			return d
		}
		return 0
	}

	switch {
	case fid == q:
		return 1000
	case bare == q:
		return 900 + tightness(bare)
	case strings.HasPrefix(bare, q):
		return 800 + tightness(bare)
	case strings.HasPrefix(fid, q):
		return 750 + tightness(fid)
	case strings.Contains(fid, q):
		// Penalise how far in the match sits: a hit at the start of the model
		// name beats one buried in a vendor prefix.
		return 700 + tightness(fid) - min(strings.Index(fid, q), 40)
	case fname != "" && strings.Contains(fname, q):
		return 600 + tightness(fname) - min(strings.Index(fname, q), 40)
	case subsequence(q, fid):
		return 400
	case fname != "" && subsequence(q, fname):
		return 300
	}
	return -1
}

// subsequence reports whether every rune of q appears in hay in order. It is
// the loosest match tier — "gl52" finds "glm-5.2" — and deliberately ignores
// gap size, because the score tier already ranks it below every substring
// match.
func subsequence(q, hay string) bool {
	if q == "" {
		return true
	}
	i := 0
	for j := 0; j < len(hay) && i < len(q); j++ {
		if hay[j] == q[i] {
			i++
		}
	}
	return i == len(q)
}

// Search matches query against every route and merges the hits into groups,
// best match first. Within a group the routes are ordered the way a user
// chooses between them: free first, then by cheapest published input rate,
// with unpriced routes (credit-billed gateways) after the priced ones — an
// unpriced route must never head a list read as "cheapest first".
//
// limit caps the number of groups returned; <= 0 means no cap.
func Search(query string, routes []Route, limit int) []Group {
	byKey := map[string]*Group{}
	var order []string
	for _, r := range routes {
		if strings.TrimSpace(r.Model.ID) == "" {
			continue
		}
		score := Match(query, r.Model.ID, r.Model.Name)
		if score < 0 {
			continue
		}
		key := identity(r.Model.ID)
		g, ok := byKey[key]
		if !ok {
			g = &Group{Key: key, Title: routeTitle(r), Score: score}
			byKey[key] = g
			order = append(order, key)
		}
		if score > g.Score {
			// The best-matching route also supplies the display name, so a
			// group headed by an exact hit is titled the way the user typed it.
			g.Score = score
			g.Title = routeTitle(r)
		}
		g.Routes = append(g.Routes, r)
	}

	out := make([]Group, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		sortRoutes(g.Routes)
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		// Ties break toward what a user reaching for a model wants first: a
		// free route, then more routes (more real choice), then name.
		if fi, fj := out[i].HasFree(), out[j].HasFree(); fi != fj {
			return fi
		}
		if len(out[i].Routes) != len(out[j].Routes) {
			return len(out[i].Routes) > len(out[j].Routes)
		}
		return out[i].Title < out[j].Title
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// routeTitle is the display name for a route: the friendly name when the
// gateway supplies a distinct one, the raw id otherwise.
func routeTitle(r Route) string {
	if r.Model.Name != "" && r.Model.Name != r.Model.ID {
		return r.Model.Name
	}
	return r.Model.ID
}

// sortRoutes orders a group's routes: free first, then cheapest published
// input rate, unpriced last, then provider name for stability.
func sortRoutes(rs []Route) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		af := a.Model.Free || a.Model.Price.Free()
		bf := b.Model.Free || b.Model.Price.Free()
		if af != bf {
			return af
		}
		if a.Model.Price.Known != b.Model.Price.Known {
			return a.Model.Price.Known
		}
		if a.Model.Price.Known && a.Model.Price.PromptUSD != b.Model.Price.PromptUSD {
			return a.Model.Price.PromptUSD < b.Model.Price.PromptUSD
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Model.ID < b.Model.ID
	})
}
