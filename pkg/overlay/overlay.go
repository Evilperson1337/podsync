package overlay

import (
	"context"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

// Provider enriches builder-produced feed metadata without changing media
// extraction ownership.
type Provider interface {
	Name() string
	Apply(ctx context.Context, cfg *feed.Config, result *model.Feed) error
}

type Manager struct {
	providers []Provider
}

func NewManager(providers ...Provider) *Manager {
	return &Manager{providers: providers}
}

func NewDefaultManager(client *http.Client) *Manager {
	return NewManager(NewRSSProvider(client))
}

func (m *Manager) Apply(ctx context.Context, cfg *feed.Config, result *model.Feed) error {
	if m == nil {
		return nil
	}

	for _, provider := range m.providers {
		if provider == nil {
			continue
		}

		if err := provider.Apply(ctx, cfg, result); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"feed_id":  cfg.ID,
				"provider": provider.Name(),
			}).Warn("metadata overlay provider failed; continuing with source metadata")
		}
	}

	return nil
}
