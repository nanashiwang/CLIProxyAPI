package pricing

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpfetch"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultCatalogURL is the upstream LiteLLM-compatible model pricing catalog.
	DefaultCatalogURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
	defaultRefresh    = 3 * time.Hour
	minimumRefresh    = 10 * time.Minute
	maximumCatalog    = 8 << 20
)

//go:embed fallback.json
var embeddedFallback []byte

var errConfigurationChanged = errors.New("pricing configuration changed during refresh")

type modelEntry map[string]any

// Options configures a pricing service instance.
type Options struct {
	Enabled         bool
	SourceURL       string
	RefreshInterval time.Duration
	CachePath       string
	Overrides       map[string]config.PricingOverride
}

// Status describes the active catalog and refresh state.
type Status struct {
	Enabled            bool      `json:"enabled"`
	SourceURL          string    `json:"source_url"`
	ActiveSource       string    `json:"active_source"`
	Version            string    `json:"version"`
	ModelCount         int       `json:"model_count"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastRefreshAttempt time.Time `json:"last_refresh_attempt,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
	RefreshInterval    string    `json:"refresh_interval"`
	CachePath          string    `json:"cache_path,omitempty"`
	CustomModelCount   int       `json:"custom_model_count"`
}

// ModelSummary is a compact management view of one catalog entry.
type ModelSummary struct {
	Model                   string  `json:"model"`
	Provider                string  `json:"provider,omitempty"`
	InputUSDPerMillion      float64 `json:"input_usd_per_million_tokens"`
	OutputUSDPerMillion     float64 `json:"output_usd_per_million_tokens"`
	CacheReadUSDPerMillion  float64 `json:"cache_read_usd_per_million_tokens"`
	CacheWriteUSDPerMillion float64 `json:"cache_write_usd_per_million_tokens"`
	CustomOverride          bool    `json:"custom_override"`
}

// Service manages model pricing data and calculates request-time costs.
type Service struct {
	mu sync.RWMutex

	catalog      map[string]modelEntry
	version      string
	activeSource string
	updatedAt    time.Time
	lastAttempt  time.Time
	lastError    string
	options      Options
	loadedCache  string
	generation   uint64

	refreshMu  sync.Mutex
	startMu    sync.Mutex
	running    bool
	configureC chan struct{}
	client     httpfetch.Doer
}

// NewService constructs a pricing service with the embedded fallback catalog loaded.
func NewService() *Service {
	s := &Service{
		configureC: make(chan struct{}, 1),
		client:     &http.Client{Timeout: 30 * time.Second},
	}
	s.options = normalizeOptions(Options{Enabled: true})
	if errLoad := s.applyCatalog(embeddedFallback, "embedded"); errLoad != nil {
		log.WithError(errLoad).Warn("pricing: failed to load embedded fallback catalog")
	}
	return s
}

var defaultService = NewService()

// Default returns the process-wide pricing service.
func Default() *Service { return defaultService }

// ConfigureDefault configures the process-wide pricing service and installs it as the usage billing calculator.
func ConfigureDefault(cfg config.PricingConfig, configPath string) {
	defaultService.Configure(OptionsFromConfig(cfg, configPath))
	coreusage.SetBillingCalculator(defaultService)
}

// StartDefaultUpdater starts background refresh for the process-wide service.
func StartDefaultUpdater(ctx context.Context) { defaultService.Start(ctx) }

// OptionsFromConfig converts application config into pricing service options.
func OptionsFromConfig(cfg config.PricingConfig, configPath string) Options {
	refresh := time.Duration(cfg.RefreshIntervalMinutes) * time.Minute
	cachePath := strings.TrimSpace(cfg.CacheFile)
	baseDir := "."
	if trimmedConfigPath := strings.TrimSpace(configPath); trimmedConfigPath != "" {
		baseDir = filepath.Dir(trimmedConfigPath)
	}
	if cachePath == "" {
		cachePath = filepath.Join(baseDir, "data", "model_pricing.json")
	} else if !filepath.IsAbs(cachePath) {
		cachePath = filepath.Join(baseDir, cachePath)
	}
	return Options{
		Enabled:         cfg.Enabled,
		SourceURL:       cfg.SourceURL,
		RefreshInterval: refresh,
		CachePath:       filepath.Clean(cachePath),
		Overrides:       cfg.Overrides,
	}
}

func normalizeOptions(options Options) Options {
	options.SourceURL = strings.TrimSpace(options.SourceURL)
	if options.SourceURL == "" {
		options.SourceURL = DefaultCatalogURL
	}
	if options.RefreshInterval <= 0 {
		options.RefreshInterval = defaultRefresh
	} else if options.RefreshInterval < minimumRefresh {
		options.RefreshInterval = minimumRefresh
	}
	options.CachePath = strings.TrimSpace(options.CachePath)
	options.Overrides = cloneOverrides(options.Overrides)
	return options
}

func cloneOverrides(source map[string]config.PricingOverride) map[string]config.PricingOverride {
	if len(source) == 0 {
		return nil
	}
	out := make(map[string]config.PricingOverride, len(source))
	for model, override := range source {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		out[model] = cloneOverride(override)
	}
	return out
}

func cloneOverride(override config.PricingOverride) config.PricingOverride {
	cloneFloat := func(value *float64) *float64 {
		if value == nil {
			return nil
		}
		cloned := *value
		return &cloned
	}
	return config.PricingOverride{
		Provider:   strings.TrimSpace(override.Provider),
		Input:      cloneFloat(override.Input),
		Output:     cloneFloat(override.Output),
		CacheRead:  cloneFloat(override.CacheRead),
		CacheWrite: cloneFloat(override.CacheWrite),
	}
}

// Configure updates runtime options and loads a valid local cache when the path changes.
func (s *Service) Configure(options Options) {
	if s == nil {
		return
	}
	options = normalizeOptions(options)

	s.mu.Lock()
	previous := s.options
	loadCache := options.CachePath != "" && options.CachePath != s.loadedCache
	refreshInvalidated := previous.Enabled != options.Enabled || previous.SourceURL != options.SourceURL || previous.CachePath != options.CachePath
	s.options = options
	if refreshInvalidated {
		s.generation++
	}
	if loadCache {
		s.loadedCache = options.CachePath
	}
	s.mu.Unlock()

	if loadCache {
		raw, errRead := os.ReadFile(options.CachePath)
		if errRead != nil {
			if !errors.Is(errRead, os.ErrNotExist) {
				log.WithError(errRead).Debug("pricing: failed to read local catalog cache")
			}
		} else if errApply := s.applyCatalog(raw, "cache"); errApply != nil {
			log.WithError(errApply).Warn("pricing: ignored invalid local catalog cache")
		}
	}

	if refreshInvalidated || previous.RefreshInterval != options.RefreshInterval {
		s.signalConfigurationChange()
	}
}

func (s *Service) signalConfigurationChange() {
	if s == nil || s.configureC == nil {
		return
	}
	s.startMu.Lock()
	running := s.running
	s.startMu.Unlock()
	if !running {
		return
	}
	select {
	case s.configureC <- struct{}{}:
	default:
	}
}

// Start refreshes immediately and then follows the configured interval until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.startMu.Lock()
	if s.running {
		s.startMu.Unlock()
		return
	}
	s.running = true
	s.startMu.Unlock()

	go func() {
		defer func() {
			s.startMu.Lock()
			s.running = false
			s.startMu.Unlock()
		}()
		refresh := func(label string) {
			if !s.isEnabled() {
				return
			}
			if errRefresh := s.Refresh(ctx); errRefresh != nil && ctx.Err() == nil && !errors.Is(errRefresh, errConfigurationChanged) {
				log.WithError(errRefresh).Warnf("pricing: %s refresh failed; keeping cached or embedded catalog", label)
			}
		}
		refresh("startup")
		for {
			interval := s.refreshInterval()
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-s.configureC:
				if !timer.Stop() {
					<-timer.C
				}
				refresh("configuration")
			case <-timer.C:
				refresh("periodic")
			}
		}
	}()
}

func (s *Service) isEnabled() bool {
	s.mu.RLock()
	enabled := s.options.Enabled
	s.mu.RUnlock()
	return enabled
}

func (s *Service) refreshInterval() time.Duration {
	s.mu.RLock()
	interval := s.options.RefreshInterval
	s.mu.RUnlock()
	if interval <= 0 {
		return defaultRefresh
	}
	return interval
}

// Refresh downloads and activates the latest valid catalog.
func (s *Service) Refresh(ctx context.Context) error {
	if s == nil {
		return errors.New("pricing service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.Lock()
	s.lastAttempt = time.Now().UTC()
	options := s.options
	generation := s.generation
	s.mu.Unlock()
	if !options.Enabled {
		return errors.New("pricing is disabled")
	}

	raw, errFetch := httpfetch.GetBytes(ctx, s.client, options.SourceURL, map[string]string{
		"Accept":     "application/json",
		"User-Agent": "CLIProxyAPI-pricing",
	}, maximumCatalog)
	if errFetch != nil {
		s.setRefreshError(errFetch)
		return errFetch
	}
	catalog, version, errParse := decodeCatalog(raw)
	if errParse != nil {
		s.setRefreshError(errParse)
		return errParse
	}
	s.mu.Lock()
	if generation != s.generation || options.SourceURL != s.options.SourceURL || !s.options.Enabled {
		s.mu.Unlock()
		return errConfigurationChanged
	}
	s.catalog = catalog
	s.version = version
	s.activeSource = publicCatalogSource(options.SourceURL)
	s.updatedAt = time.Now().UTC()
	s.lastError = ""
	s.mu.Unlock()
	if options.CachePath != "" {
		if errWrite := writeCacheFile(options.CachePath, raw); errWrite != nil {
			log.WithError(errWrite).Warn("pricing: failed to persist catalog cache")
		}
	}
	return nil
}

func (s *Service) setRefreshError(err error) {
	s.mu.Lock()
	if err != nil {
		s.lastError = err.Error()
	}
	s.mu.Unlock()
}

func writeCacheFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return fmt.Errorf("create cache directory: %w", errMkdir)
	}
	file, errCreate := os.CreateTemp(dir, ".model-pricing-*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create temporary cache: %w", errCreate)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if errChmod := file.Chmod(0o644); errChmod != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temporary cache: %w", errChmod)
	}
	if _, errWrite := file.Write(raw); errWrite != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary cache: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary cache: %w", errSync)
	}
	if errClose := file.Close(); errClose != nil {
		return fmt.Errorf("close temporary cache: %w", errClose)
	}
	if errRename := os.Rename(tempPath, path); errRename != nil {
		if runtime.GOOS == "windows" {
			if errRemove := os.Remove(path); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				return fmt.Errorf("remove old cache: %w", errRemove)
			}
			if errRenameRetry := os.Rename(tempPath, path); errRenameRetry == nil {
				removeTemp = false
				return nil
			} else {
				return fmt.Errorf("replace cache: %w", errRenameRetry)
			}
		}
		return fmt.Errorf("replace cache: %w", errRename)
	}
	removeTemp = false
	return nil
}

func (s *Service) applyCatalog(raw []byte, source string) error {
	catalog, version, errParse := decodeCatalog(raw)
	if errParse != nil {
		return errParse
	}

	s.mu.Lock()
	s.catalog = catalog
	s.version = version
	s.activeSource = publicCatalogSource(source)
	s.updatedAt = time.Now().UTC()
	s.lastError = ""
	s.mu.Unlock()
	return nil
}

func decodeCatalog(raw []byte) (map[string]modelEntry, string, error) {
	catalog, errParse := parseCatalog(raw)
	if errParse != nil {
		return nil, "", errParse
	}
	digest := sha256.Sum256(raw)
	return catalog, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func parseCatalog(raw []byte) (map[string]modelEntry, error) {
	var catalog map[string]modelEntry
	if errUnmarshal := json.Unmarshal(raw, &catalog); errUnmarshal != nil {
		return nil, fmt.Errorf("parse pricing catalog: %w", errUnmarshal)
	}
	if len(catalog) == 0 {
		return nil, errors.New("pricing catalog is empty")
	}
	for model, entry := range catalog {
		if strings.TrimSpace(model) == "" || entry == nil {
			delete(catalog, model)
			continue
		}
		_, hasInput := entryNumber(entry, "input_cost_per_token")
		_, hasOutput := entryNumber(entry, "output_cost_per_token")
		if !hasInput && !hasOutput {
			delete(catalog, model)
		}
	}
	if len(catalog) == 0 {
		return nil, errors.New("pricing catalog contains no priced models")
	}
	return catalog, nil
}

// Status returns a consistent snapshot of the current pricing service state.
func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.RLock()
	status := Status{
		Enabled:            s.options.Enabled,
		SourceURL:          publicCatalogSource(s.options.SourceURL),
		ActiveSource:       s.activeSource,
		Version:            s.version,
		ModelCount:         len(s.catalog),
		UpdatedAt:          s.updatedAt,
		LastRefreshAttempt: s.lastAttempt,
		LastError:          s.lastError,
		RefreshInterval:    s.options.RefreshInterval.String(),
		CachePath:          s.options.CachePath,
		CustomModelCount:   len(s.options.Overrides),
	}
	s.mu.RUnlock()
	return status
}

func publicCatalogSource(source string) string {
	source = strings.TrimSpace(source)
	parsed, errParse := url.Parse(source)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return source
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// ListModels returns compact, sorted pricing rows matching query.
func (s *Service) ListModels(query string, limit int) []ModelSummary {
	if s == nil {
		return nil
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 200
	} else if limit > 1000 {
		limit = 1000
	}

	s.mu.RLock()
	catalog := s.catalog
	overrides := cloneOverrides(s.options.Overrides)
	s.mu.RUnlock()

	names := make(map[string]struct{}, len(catalog)+len(overrides))
	for model := range catalog {
		names[model] = struct{}{}
	}
	for model := range overrides {
		names[model] = struct{}{}
	}
	sortedNames := make([]string, 0, len(names))
	for model := range names {
		if query != "" && !strings.Contains(strings.ToLower(model), query) {
			provider := entryString(catalog[model], "litellm_provider")
			if !strings.Contains(strings.ToLower(provider), query) {
				continue
			}
		}
		sortedNames = append(sortedNames, model)
	}
	sort.Strings(sortedNames)
	if len(sortedNames) > limit {
		sortedNames = sortedNames[:limit]
	}

	rows := make([]ModelSummary, 0, len(sortedNames))
	for _, model := range sortedNames {
		entry := catalog[model]
		override, custom := overrides[model]
		provider := entryString(entry, "litellm_provider")
		if custom && override.Provider != "" {
			provider = override.Provider
		}
		input, _ := entryNumber(entry, "input_cost_per_token")
		output, _ := entryNumber(entry, "output_cost_per_token")
		cacheRead, _ := entryNumber(entry, "cache_read_input_token_cost")
		cacheWrite, _ := entryNumber(entry, "cache_creation_input_token_cost")
		if custom {
			input = overridePerToken(override.Input, input)
			output = overridePerToken(override.Output, output)
			cacheRead = overridePerToken(override.CacheRead, cacheRead)
			cacheWrite = overridePerToken(override.CacheWrite, cacheWrite)
		}
		rows = append(rows, ModelSummary{
			Model:                   model,
			Provider:                provider,
			InputUSDPerMillion:      input * 1_000_000,
			OutputUSDPerMillion:     output * 1_000_000,
			CacheReadUSDPerMillion:  cacheRead * 1_000_000,
			CacheWriteUSDPerMillion: cacheWrite * 1_000_000,
			CustomOverride:          custom,
		})
	}
	return rows
}

func overridePerToken(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value / 1_000_000
}

func entryNumber(entry modelEntry, field string) (float64, bool) {
	if entry == nil {
		return 0, false
	}
	value, ok := entry[field]
	if !ok {
		return 0, false
	}
	number, ok := value.(float64)
	return number, ok && number >= 0
}

func entryString(entry modelEntry, field string) string {
	if entry == nil {
		return ""
	}
	value, _ := entry[field].(string)
	return strings.TrimSpace(value)
}
