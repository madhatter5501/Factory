package provider

import (
	"fmt"
	"sync"
)

// Factory creates and caches provider instances.
type Factory struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewFactory creates a new provider factory.
func NewFactory() *Factory {
	return &Factory{
		providers: make(map[string]Provider),
	}
}

// GetProvider returns a provider by name, creating it if necessary.
func (f *Factory) GetProvider(name string) (Provider, error) {
	f.mu.RLock()
	if p, ok := f.providers[name]; ok {
		f.mu.RUnlock()
		return p, nil
	}
	f.mu.RUnlock()

	// Create provider
	f.mu.Lock()
	defer f.mu.Unlock()

	// Double-check after acquiring write lock
	if p, ok := f.providers[name]; ok {
		return p, nil
	}

	var p Provider
	var err error

	switch name {
	case "anthropic":
		p, err = NewAnthropicProvider()
	case "openai":
		p, err = NewOpenAIProvider()
	case "google":
		p, err = NewGoogleProvider()
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create provider %s: %w", name, err)
	}

	f.providers[name] = p
	return p, nil
}

// GetAvailableProviders returns info about all providers with availability status.
func (f *Factory) GetAvailableProviders() []ProviderInfo {
	providers := AllProviders()

	// Update availability status
	for i := range providers {
		p, err := f.GetProvider(providers[i].Name)
		if err == nil {
			providers[i].Available = p.Available()
		}
	}

	return providers
}

// GetAllUsage returns token usage for all providers.
func (f *Factory) GetAllUsage() map[string]TokenUsage {
	f.mu.RLock()
	defer f.mu.RUnlock()

	usage := make(map[string]TokenUsage)
	for name, p := range f.providers {
		usage[name] = p.GetUsage()
	}
	return usage
}

// ResetAllUsage clears usage statistics for all providers.
func (f *Factory) ResetAllUsage() {
	f.mu.RLock()
	defer f.mu.RUnlock()

	for _, p := range f.providers {
		p.ResetUsage()
	}
}
