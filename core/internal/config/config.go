// Package config manages the daemon's settings file: ~/.config/dcode/config.json
//
// Kept apart from the UI's ui-settings.json on purpose:
//   - ui-settings.json holds appearance and preferences, read and written by QML
//   - config.json holds providers / models / API keys, read only by the daemon
//
// Models hang off providers because whether a model works depends on whether its provider has a
// key. The global model id is "provider:model" (deepseek:deepseek-chat); the UI knows only that
// string, so switching providers does not disturb existing sessions.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Provider is one API endpoint plus the models fetched from it.
type Provider struct {
	// Type picks the client: "openai" or "anthropic".
	Type string `json:"type,omitempty"`
	// Label is the display name; id when empty.
	Label string `json:"label,omitempty"`
	// Icon is the UI's icon key; Type when empty.
	Icon string `json:"icon,omitempty"`
	// BaseURL has no trailing slash, e.g. https://api.deepseek.com/v1.
	BaseURL string `json:"baseUrl"`
	// APIKey lives in the file itself (mode 600). Empty falls back to APIKeyEnv.
	APIKey string `json:"apiKey,omitempty"`
	// APIKeyEnv is an environment variable name, ignored when APIKey is set.
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// Models are this provider's models, keyed by the provider-side model id.
	Models map[string]ModelEntry `json:"models,omitempty"`
	// NativeSearch decides whether the provider's own server-side search is used (OpenAI's web_search,
	// Gemini's google_search, Anthropic's web_search server tool). nil means on: it saves a tool round
	// trip against fetching pages ourselves and returns pre-digested results. It is billed separately
	// (Anthropic: $10 per 1000 searches), hence the way to turn it off.
	NativeSearch *bool `json:"nativeSearch,omitempty"`
	// FetchedAt is when the model list was last fetched successfully (Unix ms).
	FetchedAt int64 `json:"fetchedAt,omitempty"`
}

// ModelEntry is one model under a provider.
type ModelEntry struct {
	Label   string `json:"label,omitempty"`
	Context int    `json:"contextWindow,omitempty"`
	// MaxOutput is this model's own output ceiling in tokens; 0 means unknown. Some providers report it
	// on the model list (OpenRouter's top_provider.max_completion_tokens), the rest can be set by hand.
	// It exists so a cap chosen with a big model in mind is lowered for a small one instead of being
	// sent as a 400: "256k" is a wish, not a capability.
	MaxOutput int `json:"maxOutput,omitempty"`
	// Enabled decides whether it appears in the home model picker.
	Enabled bool `json:"enabled"`
	// Reasoning overrides the built-in level table (see reasoning.go): absent uses the table, an empty
	// array means unsupported (table off), a list means exactly those. The field exists because the
	// table can only guess, and a wrong guess has to be overridable by the user.
	Reasoning []string `json:"reasoning,omitempty"`
}

// ResolveKey returns the key actually used and where it came from (for display).
func (p Provider) ResolveKey() (string, string) {
	if strings.TrimSpace(p.APIKey) != "" {
		return strings.TrimSpace(p.APIKey), "config"
	}
	name := p.APIKeyEnv
	if name != "" {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, "env:" + name
		}
	}
	return "", ""
}

// NeedsKey means this endpoint is unusable without a key (local endpoints are not).
// SupportsNativeSearch reports whether this kind of provider can search server-side.
//
// Each wire format has its own shape: Responses' web_search, Gemini's google_search, Anthropic's
// web_search server tool. Compatible endpoints (/chat/completions) have nothing like it, so
// deepseek / openrouter / Ollama answer false — sending it anyway is a 400.
func SupportsNativeSearch(typ string) bool {
	switch typ {
	case "openai-responses", "gemini", "anthropic":
		return true
	}
	return false
}

func (p Provider) NeedsKey() bool {
	return !IsLocalURL(p.BaseURL)
}

// Ready means this provider can be sent requests now.
func (p Provider) Ready() bool {
	key, _ := p.ResolveKey()
	return key != "" || !p.NeedsKey()
}

// knownLabels are display names for common providers. Older configs have no label field, and this
// table keeps them from showing as lowercase ids.
var knownLabels = map[string]string{
	"deepseek":   "DeepSeek",
	"openai":     "OpenAI",
	"anthropic":  "Anthropic",
	"google":     "Google Gemini",
	"openrouter": "OpenRouter",
	"ollama":     "Ollama",
	"custom":     "Custom",
	"local":      "Local",
}

// DisplayName is the name the UI shows: label, then template, then the built-in table.
func (p Provider) DisplayName(id string) string {
	if strings.TrimSpace(p.Label) != "" {
		return p.Label
	}
	if t, ok := TemplateByID(id); ok {
		return t.Label
	}
	if name, ok := knownLabels[id]; ok {
		return name
	}
	// custom-2 and friends: numbered, keeping the parent's name.
	if base := baseProviderID(id); base != id {
		if name, ok := knownLabels[base]; ok {
			return name
		}
	}
	return id
}

// baseProviderID turns "custom-2" back into "custom".
func baseProviderID(id string) string {
	if i := strings.LastIndex(id, "-"); i > 0 {
		tail := id[i+1:]
		allDigits := tail != ""
		for _, r := range tail {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return id[:i]
		}
	}
	return id
}

// Resolved is "one model under one provider", flattened and ready to use.
type Resolved struct {
	ID        string `json:"id"` // provider:model
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Label     string `json:"label"`
	Context   int    `json:"contextWindow,omitempty"`
	MaxOutput int    `json:"maxOutput,omitempty"`
	Enabled   bool   `json:"enabled"`
	// Reasoning lists this model's levels; empty means unsupported (no menu). Computed here because the
	// decision needs the provider's baseURL, and a caller holding a Resolved (agent, configView) does
	// not necessarily have it.
	Reasoning []string `json:"-"`
}

// Config is the whole file.
type Config struct {
	Version          int                 `json:"version,omitempty"`
	Providers        map[string]Provider `json:"providers"`
	DefaultModel     string              `json:"defaultModel,omitempty"`
	WorkingDirectory string              `json:"workingDirectory,omitempty"`
	SystemPrompt     string              `json:"systemPrompt,omitempty"`
	// MaxOutputTokens is the per-request cap; 0 leaves it to the provider (Anthropic always needs a
	// number, so it falls back to 4096 there).
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
	// MaxToolSteps caps model calls per turn (one tool round trip is a step; several tools in one
	// response are not more steps). 0 means no cap. A fuse against looping, not a budget: 20–60 steps
	// are normal in real work, and hitting it means the model is circling, not that you did too much.
	MaxToolSteps int `json:"maxToolSteps,omitempty"`
	// CompactAt is the compaction threshold (estimated tokens); 0 disables it. It measures the history
	// about to be sent, not the cumulative usage on the bill.
	CompactAt int `json:"compactAt,omitempty"`
	// ApprovalMode decides whether a tool call asks first:
	//   "ask"        always ask (default)
	//   "workspace"  auto-approve what clearly stays inside the working directory, ask otherwise
	//   "auto"       never ask
	ApprovalMode string  `json:"approvalMode,omitempty"`
	Temperature  float64 `json:"temperature,omitempty"`

	// Tools says whether a class of tool is offered to the model. A disabled tool never appears in the
	// list, so the model stops trying to call it.
	Tools ToolSwitches `json:"tools,omitempty"`
	// Browser configures the browser tool (agent-browser).
	//
	// Kept apart from Tools because it holds a path as well as a switch, and that path is an install
	// location rather than a switch; folding it into ToolSwitches would make that struct a junk
	// drawer.
	Browser BrowserSettings `json:"browser,omitempty"`
	// Search is the web-search backend. Stored apart from Providers on purpose: its shape (query →
	// results) has nothing to do with chat, and mixing them complicates both.
	Search        map[string]SearchProvider `json:"search,omitempty"`
	SearchDefault string                    `json:"searchDefault,omitempty"`

	// MCPServers are external tool servers (Model Context Protocol, stdio). Separate from Provider:
	// those say where the model comes from, these say what it can do.
	MCPServers map[string]MCPServer `json:"mcpServers,omitempty"`

	// v1 kept models at the top level. Read only during migration, then cleared.
	LegacyModels []legacyModel `json:"models,omitempty"`
}

const (
	// DefaultMaxToolSteps is the per-turn step cap when unset.
	//
	// 200 is deliberately far above normal use: a real session was measured at 116 model calls in a
	// single turn without circling at all, so the previous 100 would have cut an ordinary turn short —
	// and the closing round it forces (no tools) is where a model tends to spill raw tool syntax as
	// text. Hitting the cap still means the model is circling, and stopping is still right.
	DefaultMaxToolSteps = 200
	// MaxToolStepsLimit is the cap on the cap. Higher is not a guard anymore, it is giving up.
	MaxToolStepsLimit = 1000
	// MaxOutputCap is the largest output cap the settings API accepts, in tokens — the top entry of the
	// dropdown. Anything above it is a typo rather than a wish.
	MaxOutputCap = 262144
)

// ToolStepLimit returns how many steps this turn may take; 0 means no cap.
//
// A negative value (only reachable by editing config.json by hand) is treated as the default rather
// than as "off": a typo must not silently remove the fuse. Callers must therefore check for 0.
func (c *Config) ToolStepLimit() int {
	if c == nil {
		return DefaultMaxToolSteps
	}
	switch {
	case c.MaxToolSteps < 0:
		return DefaultMaxToolSteps
	case c.MaxToolSteps > MaxToolStepsLimit:
		return MaxToolStepsLimit
	}
	return c.MaxToolSteps
}

// OutputCap is the output-token cap actually sent for this model.
//
// 0 stays 0: "off" means the provider decides, and inventing a number there would replace a working
// default with a guess. A number the user chose is lowered to the model's own ceiling, because a
// provider rejects max_tokens above the model's maximum outright — "256k" with an 8k model is a
// failed request, not a generous one. An unknown ceiling (0) leaves the choice alone.
func (c *Config) OutputCap(model Resolved) int {
	if c == nil || c.MaxOutputTokens <= 0 {
		return 0
	}
	if limit := model.MaxOutput; limit > 0 && c.MaxOutputTokens > limit {
		return limit
	}
	return c.MaxOutputTokens
}

// compactContextShare is how much of the model's context window the history may fill.
//
// A compaction threshold above what the model can read is not "compact less", it is "never compact,
// then fail": the request that would have crossed it is already too large to send, and an overflow is
// a 400 the daemon does not retry. So the setting is treated as a ceiling to stay under, never as a
// promise. The remaining fifth is headroom for the reply and for the estimate being an estimate.
const compactContextShare = 0.8

// CompactThreshold is the history size at which compaction fires for this model.
//
// The configured number is used as given while it fits inside the model's window, and lowered when it
// does not: a model that reads 128k cannot honour a 512k threshold, it can only overflow.
func (c *Config) CompactThreshold(model Resolved) int {
	if c == nil || c.CompactAt <= 0 {
		return 0
	}
	if model.Context <= 0 {
		return c.CompactAt
	}
	if safe := int(float64(model.Context) * compactContextShare); safe < c.CompactAt {
		return safe
	}
	return c.CompactAt
}

// legacyModel is the v1 flat model table, kept only for migration.
type legacyModel struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Context  int    `json:"contextWindow"`
}

// ModelID builds the global model id.
func ModelID(provider, model string) string { return provider + ":" + model }

// SplitModelID splits the global id; without a colon the provider is empty.
func SplitModelID(id string) (string, string) {
	if i := strings.Index(id, ":"); i > 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// Resolve finds a model. id may be "provider:model" or empty (use the default).
func (c *Config) Resolve(id string) (Resolved, Provider, bool) {
	if strings.TrimSpace(id) == "" {
		id = c.DefaultModel
	}
	providerID, modelID := SplitModelID(id)
	if providerID == "" {
		// Model name only: scan for the provider that has it.
		for pid, p := range c.Providers {
			if _, ok := p.Models[modelID]; ok {
				providerID = pid
				break
			}
		}
		if providerID == "" {
			return Resolved{}, Provider{}, false
		}
	}

	p, ok := c.Providers[providerID]
	if !ok {
		return Resolved{}, Provider{}, false
	}
	entry, ok := p.Models[modelID]
	if !ok {
		return Resolved{}, Provider{}, false
	}

	label := entry.Label
	if label == "" {
		label = modelID
	}
	return Resolved{
		ID:        ModelID(providerID, modelID),
		Provider:  providerID,
		Model:     modelID,
		Label:     label,
		Context:   entry.Context,
		MaxOutput: entry.MaxOutput,
		Enabled:   entry.Enabled,
		Reasoning: ReasoningLevels(p, modelID, entry.Reasoning),
	}.withLimits(p), p, true
}

// AllModels returns every provider's models, sorted by provider then model name.
func (c *Config) AllModels() []Resolved {
	out := make([]Resolved, 0, 16)
	for _, pid := range c.ProviderIDs() {
		p := c.Providers[pid]
		names := make([]string, 0, len(p.Models))
		for name := range p.Models {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := p.Models[name]
			label := entry.Label
			if label == "" {
				label = name
			}
			out = append(out, Resolved{
				ID:        ModelID(pid, name),
				Provider:  pid,
				Model:     name,
				Label:     label,
				Context:   entry.Context,
				MaxOutput: entry.MaxOutput,
				Enabled:   entry.Enabled,
				Reasoning: ReasoningLevels(p, name, entry.Reasoning),
			}.withLimits(p))
		}
	}
	return out
}

// EnabledModels is what the home model picker offers.
func (c *Config) EnabledModels() []Resolved {
	all := c.AllModels()
	out := make([]Resolved, 0, len(all))
	for _, m := range all {
		if m.Enabled {
			out = append(out, m)
		}
	}
	return out
}

// ProviderIDs returns the provider ids, sorted.
func (c *Config) ProviderIDs() []string {
	ids := make([]string, 0, len(c.Providers))
	for id := range c.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Dir is the config directory ~/.config/dcode.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "dcode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "dcode")
}

// Path is the full path of config.json.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Load reads the config; when the file is missing it writes out defaults and returns them.
func Load() (*Config, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		cfg := Default()
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	migrated := cfg.applyDefaults()
	if migrated {
		// A migrated config must be written back, or the next start migrates again.
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// Save writes the file (mode 600).
//
// Keep a copy of the old file first: it is the user's only source of keys, and one bad write makes
// them start over.
func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	// A copy is only kept when the old file had content and the new one is empty; otherwise every save
	// would leave one behind.
	if len(c.Providers) == 0 {
		if prev, err := os.ReadFile(Path()); err == nil && len(prev) > 200 {
			_ = os.WriteFile(Path()+".bak", prev, 0o600)
		}
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// FixDefaults recomputes derived fields (a default model pointing at nothing gets replaced). Call
// it after changing providers or model switches, then save.
func (c *Config) FixDefaults() { c.applyDefaults() }

// applyDefaults fills in missing fields and moves v1 models into providers. true means something
// changed and the file needs writing.
func (c *Config) applyDefaults() bool {
	changed := false

	if c.Providers == nil {
		c.Providers = map[string]Provider{}
		changed = true
	}
	if c.Version < 2 {
		c.Version = 2
		changed = true
	}
	// v3 made 0 meaningful for both limits ("off"), so a v2 file carrying 0 means "unset" and has to be
	// pinned to the old defaults first; after that, 0 is the user's own choice and stays.
	//
	// The step cap still gets pinned because "off" there is a real hazard (a circling model with no
	// fuse). The output cap does not: 4096 was only ever a default, it cut replies mid-sentence, and
	// the default is now "off", so a v2 zero lands on the same answer it would get on a fresh install.
	if c.Version < 3 {
		if c.MaxToolSteps == 0 {
			c.MaxToolSteps = DefaultMaxToolSteps
		}
		c.Version = 3
		changed = true
	}
	if c.MaxOutputTokens < 0 {
		c.MaxOutputTokens = 0
		changed = true
	}
	// Same reasoning as the step cap: a hand-edited number outside what the UI offers is clamped, so the
	// dropdown and the request cannot disagree.
	if c.MaxOutputTokens > MaxOutputCap {
		c.MaxOutputTokens = MaxOutputCap
		changed = true
	}
	// The step cap is normalised here too: 3 or 100000 typed into config.json is clamped, so "this is a
	// guard" is not a matter of luck. 0 (no cap) is legal and keeps its meaning.
	if c.MaxToolSteps < 0 {
		c.MaxToolSteps = 0
		changed = true
	}
	if limit := c.ToolStepLimit(); limit != c.MaxToolSteps {
		c.MaxToolSteps = limit
		changed = true
	}
	if c.WorkingDirectory == "" {
		home, _ := os.UserHomeDir()
		c.WorkingDirectory = home
		changed = true
	}
	switch c.ApprovalMode {
	case "ask", "workspace", "auto":
	default:
		// An empty string (older config) and garbage both fall back to the safest value.
		c.ApprovalMode = "ask"
		changed = true
	}

	// Search backends: fill defaults and point SearchDefault at something usable.
	if len(c.Search) > 0 {
		for id, p := range c.Search {
			if p.MaxResults <= 0 {
				p.MaxResults = 5
				changed = true
			}
			if p.MaxResults > 10 {
				p.MaxResults = 10
				changed = true
			}
			if p.Label == "" && p.Type != "" {
				p.Label = p.Type
				changed = true
			}
			c.Search[id] = p
		}
		if raw, ok := c.Search[c.SearchDefault]; !ok || !raw.Enabled {
			if id, _, ok := c.ActiveSearch(); ok {
				c.SearchDefault = id
				changed = true
			}
		}
	} else if c.SearchDefault != "" {
		c.SearchDefault = ""
		changed = true
	}

	// v1 → v2: top-level models move into their providers.
	if len(c.LegacyModels) > 0 {
		for _, m := range c.LegacyModels {
			p, ok := c.Providers[m.Provider]
			if !ok {
				p = Provider{
					Type:      "openai",
					BaseURL:   defaultBaseURL(m.Provider),
					APIKeyEnv: defaultEnvVar(m.Provider),
				}
			}
			if p.Models == nil {
				p.Models = map[string]ModelEntry{}
			}
			key := m.Model
			if key == "" {
				key = m.ID
			}
			p.Models[key] = ModelEntry{
				Label:   m.Label,
				Context: m.Context,
				Enabled: true,
			}
			c.Providers[m.Provider] = p
			if c.DefaultModel == m.ID || c.DefaultModel == "" {
				c.DefaultModel = ModelID(m.Provider, key)
			}
		}
		c.LegacyModels = nil
		changed = true
	}

	// If the default model points at nothing, take the first enabled one.
	if _, _, ok := c.Resolve(c.DefaultModel); !ok {
		if enabled := c.EnabledModels(); len(enabled) > 0 {
			c.DefaultModel = enabled[0].ID
			changed = true
		} else if c.DefaultModel != "" {
			c.DefaultModel = ""
			changed = true
		}
	}
	return changed
}

// Default is what gets written on first start.
//
// providers is empty on purpose: the first visit to settings should be "add a provider" rather than
// a pile of keyless shells. Templates() has the catalogue.
//
// MaxOutputTokens stays 0: a cap is something to opt into, and a low default silently truncates
// answers where the user cannot see the cause.
func Default() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Version:          3,
		Providers:        map[string]Provider{},
		DefaultModel:     "",
		WorkingDirectory: home,
		MaxOutputTokens:  0,
		MaxToolSteps:     DefaultMaxToolSteps,
	}
}

func defaultEnvVar(provider string) string {
	switch provider {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "google":
		return "GEMINI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	}
	return ""
}

func defaultBaseURL(provider string) string {
	if t, ok := TemplateByID(provider); ok {
		return t.BaseURL
	}
	return ""
}

// IsLocalURL reports a local endpoint (no key needed).
func IsLocalURL(u string) bool {
	return strings.Contains(u, "127.0.0.1") ||
		strings.Contains(u, "localhost") ||
		strings.Contains(u, "[::1]") ||
		strings.Contains(u, "0.0.0.0")
}

// ToolSwitches says whether a class of tool is offered to the model.
//
// *bool rather than bool: an absent entry has to count as on, and bool's zero value is false — an
// older config would silently switch every tool off.
type ToolSwitches struct {
	FileAccess *bool `json:"fileAccess,omitempty"`
	Shell      *bool `json:"shell,omitempty"`
	// WebSearch is the composer's "web search" toggle. Whether a backend is configured (capability) is
	// a separate question from whether to use it (this switch).
	WebSearch *bool `json:"webSearch,omitempty"`
}

// FileEnabled reports whether the file tools (read, write, edit, glob, grep) are on.
func (t ToolSwitches) FileEnabled() bool { return t.FileAccess == nil || *t.FileAccess }

// ShellEnabled reports whether bash is on.
func (t ToolSwitches) ShellEnabled() bool { return t.Shell == nil || *t.Shell }

// WebSearchEnabled is the "web search" switch, on by default; whether a query can run also needs a
// backend (see Config.SearchEnabled).
func (t ToolSwitches) WebSearchEnabled() bool { return t.WebSearch == nil || *t.WebSearch }

// BrowserSettings is where the browser tool lives.
//
// There is no on/off switch here: the browser is part of "web", sharing the composer's button with
// search and page fetch. One more switch would only produce "I turned web on, why is the browser
// gone?".
type BrowserSettings struct {
	// Path is the agent-browser location; empty means look in PATH.
	//
	// Being able to point at it matters because it is not always on PATH (a LobeHub-style install is
	// not), and editing the session PATH to reach it is disproportionate.
	Path string `json:"path,omitempty"`
}

// SearchProvider is one web-search backend.
//
// The three types differ only in request shape: SearXNG needs its own URL, Brave and Tavily need
// keys. Keys live in the user's config.json and never ship with the program — shipping them would
// hand the quota to everyone who downloads it.
type SearchProvider struct {
	Type string `json:"type"` // searxng | brave | tavily
	// Label is the name in the list; the type name when empty.
	Label string `json:"label,omitempty"`
	// BaseURL is only needed for a self-hosted SearXNG; empty means the official endpoint.
	BaseURL string `json:"baseURL,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	Enabled bool   `json:"enabled,omitempty"`
	// MaxResults is hits per query: default 5, max 10.
	MaxResults int `json:"maxResults,omitempty"`
}

// SearchEnabled reports whether any usable search backend exists.
func (c *Config) SearchEnabled() bool {
	_, _, ok := c.ActiveSearch()
	return ok
}

// ActiveSearch returns the backend to use.
//
// SearchDefault first; if it was deleted or disabled, the first enabled one. Sorted by id rather
// than map iteration order, or the same config would pick differently on every start.
func (c *Config) ActiveSearch() (string, SearchProvider, bool) {
	if p, ok := c.Search[c.SearchDefault]; ok && p.Enabled && p.Type != "" {
		return c.SearchDefault, p, true
	}
	ids := make([]string, 0, len(c.Search))
	for id := range c.Search {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := c.Search[id]
		if p.Enabled && p.Type != "" {
			return id, p, true
		}
	}
	return "", SearchProvider{}, false
}
