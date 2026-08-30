// Package opencode provides the runtime model capability catalog used by the
// native OpenCode provider.
package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultCapabilitiesURL     = "https://models.opencode.ai/api.json"
	DefaultZenDocsURL          = "https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/zen.mdx"
	DefaultGoDocsURL           = "https://raw.githubusercontent.com/anomalyco/opencode/dev/packages/web/src/content/docs/go.mdx"
	maxCapabilitiesBytes       = 64 << 20
	maxDocsBytes               = 8 << 20
	maxCapabilityProviders     = 4096
	maxCapabilityModelsPerTier = 4096
	maxProviderModels          = 4096
	maxModelIDBytes            = 512
	maxModelNameBytes          = 4096
	maxModelDescriptionBytes   = 16 << 10
	maxModelModalities         = 16
	maxModalityBytes           = 64
	maxModelLimit              = 1 << 31
)

type Tier string

const (
	TierZen Tier = "zen"
	TierGo  Tier = "go"
)

type Protocol string

const (
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
	ProtocolAnthropic Protocol = "anthropic"
)

// Model contains the upstream metadata needed to expose and route one model.
type Model struct {
	ID                  string
	Name                string
	Description         string
	Protocol            Protocol
	ContextLength       int
	MaxCompletionTokens int
	InputModalities     []string
	OutputModalities    []string
	Reasoning           bool
	AnonymousAllowed    bool
	Deprecated          bool
	Created             int64
}

type Snapshot struct {
	Zen       int
	Go        int
	Total     int
	UpdatedAt time.Time
	LastError string
	Ready     bool
}

type Catalog struct {
	mu          sync.RWMutex
	refreshMu   sync.Mutex
	models      map[Tier]map[string]Model
	contentHash [32]byte
	version     uint64
	updatedAt   time.Time
	lastError   string
	endpoint    string
	docs        map[Tier]string
	client      *http.Client
}

// NewCatalog creates a catalog using OpenCode's public capability endpoint.
func NewCatalog() *Catalog {
	return NewCatalogWithHTTPClient(http.DefaultClient, DefaultCapabilitiesURL)
}

// NewCatalogWithHTTPClient is primarily useful for tests and controlled deployments.
func NewCatalogWithHTTPClient(client *http.Client, endpoint string) *Catalog {
	if client == nil {
		client = http.DefaultClient
	}
	// Catalog responses never carry credentials. Still stop redirects by default
	// so a metadata endpoint cannot turn into an arbitrary internal GET target.
	clientCopy := *client
	if clientCopy.CheckRedirect == nil {
		clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultCapabilitiesURL
	}
	return &Catalog{
		models:   map[Tier]map[string]Model{TierZen: {}, TierGo: {}},
		endpoint: endpoint,
		docs:     map[Tier]string{TierZen: DefaultZenDocsURL, TierGo: DefaultGoDocsURL},
		client:   &clientCopy,
	}
}

// Start begins an initial refresh and a periodic refresh loop. The callback is
// called only after a successful snapshot replacement that changes capabilities.
func (c *Catalog) Start(ctx context.Context, interval time.Duration, onChange func()) (stop func()) {
	if c == nil {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	changeCh := make(chan struct{}, 1)
	var stopOnce sync.Once
	stop = func() {
		stopOnce.Do(func() {
			cancel()
			<-done
		})
	}
	if onChange != nil {
		// Run callbacks outside the refresh loop so a callback can safely call stop.
		go func() {
			for {
				select {
				case <-loopCtx.Done():
					return
				case <-changeCh:
					onChange()
				}
			}
		}()
	}
	notifyChange := func() {
		if onChange == nil || loopCtx.Err() != nil {
			return
		}
		select {
		case changeCh <- struct{}{}:
		default:
			// Coalesce refresh notifications while a callback is running.
		}
	}
	go func() {
		defer close(done)
		before := c.Version()
		errRefresh := c.Refresh(loopCtx)
		if errRefresh != nil && loopCtx.Err() == nil {
			// The error is retained in the snapshot for health/diagnostic callers.
		}
		if errRefresh == nil && loopCtx.Err() == nil && onChange != nil && c.Version() != before {
			// Refresh itself cannot distinguish an initial empty snapshot from a
			// change for callers that need the initial registration.
			notifyChange()
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				before := c.Version()
				if err := c.Refresh(loopCtx); err != nil {
					continue
				}
				if c.Version() != before {
					notifyChange()
				}
			}
		}
	}()
	return stop
}

// Refresh fetches a new capability snapshot. Failed refreshes keep the old snapshot.
func (c *Catalog) Refresh(ctx context.Context) error {
	if c == nil {
		return errors.New("opencode catalog is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Serialize refreshes so a slow manual refresh cannot race a periodic refresh
	// and publish an older snapshot after a newer one.
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	providers, err := c.fetchProviders(ctx)
	if err != nil {
		c.recordError(err)
		return err
	}
	c.mu.RLock()
	docs := map[Tier]string{TierZen: c.docs[TierZen], TierGo: c.docs[TierGo]}
	c.mu.RUnlock()
	parsed, err := parseProviders(ctx, c.client, providers, docs)
	if err != nil {
		c.recordError(err)
		return err
	}
	if len(parsed[TierZen])+len(parsed[TierGo]) == 0 {
		err = errors.New("OpenCode capability endpoint returned no supported models")
		c.recordError(err)
		return err
	}
	contentHash := hashModels(parsed)
	c.mu.Lock()
	if c.updatedAt.IsZero() || c.contentHash != contentHash {
		c.models = parsed
		c.contentHash = contentHash
		c.version++
		c.updatedAt = time.Now().UTC()
	}
	c.lastError = ""
	c.mu.Unlock()
	return nil
}

func (c *Catalog) fetchProviders(ctx context.Context) (map[string]provider, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("OpenCode capability endpoint returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCapabilitiesBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCapabilitiesBytes {
		return nil, fmt.Errorf("OpenCode capability response exceeds %d bytes", maxCapabilitiesBytes)
	}
	var providers map[string]provider
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, err
	}
	if len(providers) > maxCapabilityProviders {
		return nil, fmt.Errorf("OpenCode capability response contains too many providers")
	}
	for providerID, item := range providers {
		if len(item.Models) > maxProviderModels {
			return nil, fmt.Errorf("OpenCode provider %q contains too many models", truncateCatalogText(providerID, 128))
		}
	}
	return providers, nil
}

type provider struct {
	ID     string                   `json:"id"`
	API    string                   `json:"api"`
	NPM    string                   `json:"npm"`
	Models map[string]providerModel `json:"models"`
}

type providerModel struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Reasoning   bool           `json:"reasoning"`
	Modalities  modalities     `json:"modalities"`
	Limit       modelLimit     `json:"limit"`
	Cost        *modelCost     `json:"cost"`
	Provider    *modelProvider `json:"provider"`
}

type modelProvider struct {
	NPM string `json:"npm"`
}
type modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}
type modelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}
type modelCost struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

var docsProtocolPattern = regexp.MustCompile("\\|[^|]+\\|\\s*`?([^|`\\s]+)`?\\s*\\|\\s*`[^`]+/v1/(chat/completions|responses|messages)")

func parseProviders(ctx context.Context, client *http.Client, providers map[string]provider, docsByTier ...map[Tier]string) (map[Tier]map[string]Model, error) {
	result := map[Tier]map[string]Model{TierZen: {}, TierGo: {}}
	providerIDs := make([]string, 0, len(providers))
	for providerID := range providers {
		providerIDs = append(providerIDs, providerID)
	}
	// Map iteration is deliberately sorted so duplicate model IDs have a stable
	// winner instead of depending on Go's randomized map order.
	sort.Strings(providerIDs)
	for _, providerID := range providerIDs {
		item := providers[providerID]
		tier, ok := providerTier(providerID, item.ID, item.API)
		if !ok {
			continue
		}
		modelIDs := make([]string, 0, len(item.Models))
		for modelID := range item.Models {
			modelIDs = append(modelIDs, modelID)
		}
		sort.Strings(modelIDs)
		for _, modelID := range modelIDs {
			raw := item.Models[modelID]
			if raw.ID != "" {
				modelID = raw.ID
			}
			modelID = strings.TrimSpace(modelID)
			if modelID == "" || len(modelID) > maxModelIDBytes || strings.EqualFold(raw.Status, "deprecated") {
				continue
			}
			protocol, ok := protocolForSDK(item.NPM)
			if raw.Provider != nil && raw.Provider.NPM != "" {
				protocol, ok = protocolForSDK(raw.Provider.NPM)
			}
			if !ok {
				continue
			}
			if _, exists := result[tier][modelID]; exists {
				continue
			}
			if len(result[tier]) >= maxCapabilityModelsPerTier {
				return nil, fmt.Errorf("OpenCode %s catalog contains too many supported models", tier)
			}
			anonymousAllowed := strings.Contains(strings.ToLower(modelID), "free")
			if raw.Cost != nil && raw.Cost.Input == 0 && raw.Cost.Output == 0 {
				anonymousAllowed = true
			}
			result[tier][modelID] = Model{
				ID: modelID, Name: truncateCatalogText(strings.TrimSpace(raw.Name), maxModelNameBytes),
				Description: truncateCatalogText(strings.TrimSpace(raw.Description), maxModelDescriptionBytes),
				Protocol:    protocol, ContextLength: normalizeModelLimit(raw.Limit.Context),
				MaxCompletionTokens: normalizeModelLimit(raw.Limit.Output),
				InputModalities:     normalizeModalities(raw.Modalities.Input), OutputModalities: normalizeModalities(raw.Modalities.Output),
				Reasoning: raw.Reasoning, AnonymousAllowed: anonymousAllowed,
			}
		}
	}
	// The API catalog often omits per-model SDK fields. Endpoint documentation is
	// authoritative for those models and supplements the machine-readable data.
	var docsWG sync.WaitGroup
	var docsMu sync.Mutex
	docEndpoints := map[Tier]string{TierZen: DefaultZenDocsURL, TierGo: DefaultGoDocsURL}
	if len(docsByTier) > 0 && docsByTier[0] != nil {
		for tier, endpoint := range docsByTier[0] {
			if strings.TrimSpace(endpoint) != "" {
				docEndpoints[tier] = endpoint
			}
		}
	}
	for _, item := range []struct {
		tier     Tier
		endpoint string
	}{{TierZen, docEndpoints[TierZen]}, {TierGo, docEndpoints[TierGo]}} {
		item := item
		docsWG.Add(1)
		go func() {
			defer docsWG.Done()
			protocols, err := fetchDocProtocols(ctx, client, item.endpoint)
			if err != nil {
				return
			}
			docsMu.Lock()
			defer docsMu.Unlock()
			for id, protocol := range protocols {
				if model, exists := result[item.tier][id]; exists {
					model.Protocol = protocol
					result[item.tier][id] = model
				}
			}
		}()
	}
	docsWG.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func fetchDocProtocols(ctx context.Context, client *http.Client, endpoint string) (map[string]Protocol, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain, text/markdown, */*")
	req.Header.Set("User-Agent", userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("OpenCode documentation returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDocsBytes {
		return nil, errors.New("OpenCode documentation is too large")
	}
	out := make(map[string]Protocol)
	for _, line := range strings.Split(string(body), "\n") {
		match := docsProtocolPattern.FindStringSubmatch(line)
		if len(match) != 3 {
			continue
		}
		protocol := ProtocolChat
		switch match[2] {
		case "responses":
			protocol = ProtocolResponses
		case "messages":
			protocol = ProtocolAnthropic
		}
		out[strings.TrimSpace(match[1])] = protocol
	}
	if len(out) == 0 {
		return nil, errors.New("OpenCode documentation contained no protocol rows")
	}
	return out, nil
}

func providerTier(values ...string) (Tier, bool) {
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		switch value {
		case "opencode-go", "opencode_go", "https://opencode.ai/zen/go/v1":
			return TierGo, true
		case "opencode", "opencode_zen", "https://opencode.ai/zen/v1":
			return TierZen, true
		}
	}
	return "", false
}

func protocolForSDK(value string) (Protocol, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "anthropic"):
		return ProtocolAnthropic, true
	case value == "@ai-sdk/openai" || strings.HasSuffix(value, "/openai"):
		return ProtocolResponses, true
	case value == "@ai-sdk/openai-compatible" || strings.HasSuffix(value, "/openai-compatible") || strings.Contains(value, "openai-compatible"):
		return ProtocolChat, true
	default:
		return "", false
	}
}

func normalizeModelLimit(value int) int {
	if value <= 0 {
		return 0
	}
	if value > maxModelLimit {
		return maxModelLimit
	}
	return value
}

func normalizeModalities(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, minInt(len(values), maxModelModalities))
	seen := make(map[string]struct{}, minInt(len(values), maxModelModalities))
	for _, value := range values {
		value = truncateCatalogText(strings.TrimSpace(value), maxModalityBytes)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == maxModelModalities {
			break
		}
	}
	return result
}

func truncateCatalogText(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func cloneStrings(values []string) []string { return append([]string(nil), values...) }
func userAgent() string                     { return "opencode/1.18.21 (cli-proxy-api)" }

func (c *Catalog) recordError(err error) { c.mu.Lock(); c.lastError = err.Error(); c.mu.Unlock() }
func (c *Catalog) Ready() bool           { c.mu.RLock(); defer c.mu.RUnlock(); return !c.updatedAt.IsZero() }
func (c *Catalog) UpdatedAt() time.Time  { c.mu.RLock(); defer c.mu.RUnlock(); return c.updatedAt }
func (c *Catalog) Version() uint64       { c.mu.RLock(); defer c.mu.RUnlock(); return c.version }
func (c *Catalog) Protocol(tier Tier, modelID string) Protocol {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.models[tier][strings.TrimSpace(modelID)].Protocol
}
func (c *Catalog) Models(tier Tier) []Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	models := make([]Model, 0, len(c.models[tier]))
	for _, model := range c.models[tier] {
		models = append(models, cloneModel(model))
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}
func (c *Catalog) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	zen, goCount := len(c.models[TierZen]), len(c.models[TierGo])
	return Snapshot{Zen: zen, Go: goCount, Total: zen + goCount, UpdatedAt: c.updatedAt, LastError: c.lastError, Ready: !c.updatedAt.IsZero()}
}
func cloneModel(model Model) Model {
	model.InputModalities = cloneStrings(model.InputModalities)
	model.OutputModalities = cloneStrings(model.OutputModalities)
	return model
}

func hashModels(models map[Tier]map[string]Model) [32]byte {
	type item struct {
		Tier  Tier  `json:"tier"`
		Model Model `json:"model"`
	}
	items := make([]item, 0)
	for _, tier := range []Tier{TierZen, TierGo} {
		ids := make([]string, 0, len(models[tier]))
		for id := range models[tier] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			items = append(items, item{Tier: tier, Model: cloneModel(models[tier][id])})
		}
	}
	data, _ := json.Marshal(items)
	return sha256.Sum256(data)
}
