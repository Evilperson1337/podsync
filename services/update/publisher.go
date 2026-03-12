package update

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
)

type PublicationService struct {
	db        db.Storage
	fs        fs.Storage
	publisher *fs.Publisher
	feeds     map[string]*feed.Config
	hostname  string
}

func NewPublicationService(database db.Storage, storage fs.Storage, feeds map[string]*feed.Config, hostname string) *PublicationService {
	return &PublicationService{db: database, fs: storage, publisher: fs.NewPublisher(storage), feeds: feeds, hostname: hostname}
}

func (p *PublicationService) PublishFeedXML(ctx context.Context, feedConfig *feed.Config) error {
	f, err := p.db.GetFeed(ctx, feedConfig.ID)
	if err != nil {
		return err
	}

	logger := loggerWithExecution(ctx, log.Fields{"feed_id": feedConfig.ID})
	logger.Debug("building iTunes podcast feed")
	podcast, err := feed.Build(ctx, f, feedConfig, p.hostname)
	if err != nil {
		return err
	}
	xmlText, err := feed.RenderXML(podcast, f.Episodes)
	if err != nil {
		return err
	}

	reader := bytes.NewReader([]byte(xmlText))
	xmlName := fmt.Sprintf("%s.xml", feedConfig.ID)
	if _, err := p.publisher.Publish(ctx, xmlName, reader, fs.PublishOptions{MinSize: 1}); err != nil {
		return errors.Wrap(err, "failed to upload new XML feed")
	}
	metricPublicationXMLBuilds.Add(1)
	if summary, err := p.readPublicationSummary(ctx); err == nil {
		summary.LastXMLBuildAt = time.Now().UTC()
		summary.LastXMLFeedID = feedConfig.ID
		summary.XMLBuildCount++
		summary.LastPublicationAt = summary.LastXMLBuildAt
		summary.LastPublicationType = "xml"
		logger.WithFields(log.Fields{"xml_build_count": summary.XMLBuildCount, "last_xml_feed_id": summary.LastXMLFeedID}).Info("persisting publication summary after xml build")
		_ = p.db.SetPublicationSummary(ctx, summary)
	}

	if err := p.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		if !model.IsEpisodePublishable(episode.Status) {
			return nil
		}
		return p.db.UpdateEpisode(feedConfig.ID, episode.ID, func(existing *model.Episode) error {
			existing.Status = model.EpisodePublished
			return nil
		})
	}); err != nil {
		return err
	}

	return nil
}

func (p *PublicationService) PublishOPML(ctx context.Context) error {
	logger := loggerWithExecution(ctx, log.Fields{})
	logger.Debug("building podcast OPML")
	opmlText, err := feed.BuildOPML(ctx, p.feeds, p.db, p.hostname)
	if err != nil {
		return err
	}
	reader := bytes.NewReader([]byte(opmlText))
	xmlName := fmt.Sprintf("%s.opml", "podsync")
	if _, err := p.publisher.Publish(ctx, xmlName, reader, fs.PublishOptions{MinSize: 1}); err != nil {
		return errors.Wrap(err, "failed to upload OPML")
	}
	metricPublicationOPMLBuilds.Add(1)
	if summary, err := p.readPublicationSummary(ctx); err == nil {
		summary.LastOPMLBuildAt = time.Now().UTC()
		summary.OPMLBuildCount++
		summary.LastPublicationAt = summary.LastOPMLBuildAt
		summary.LastPublicationType = "opml"
		logger.WithFields(log.Fields{"opml_build_count": summary.OPMLBuildCount}).Info("persisting publication summary after opml build")
		_ = p.db.SetPublicationSummary(ctx, summary)
	}
	return nil
}

func (p *PublicationService) readPublicationSummary(ctx context.Context) (*model.PublicationSummary, error) {
	summary, err := p.db.GetPublicationSummary(ctx)
	if err == nil {
		loggerWithExecution(ctx, log.Fields{
			"xml_build_count":     summary.XMLBuildCount,
			"opml_build_count":    summary.OPMLBuildCount,
			"last_publication_at": summary.LastPublicationAt,
		}).Debug("loaded persisted publication summary")
		return summary, nil
	}
	if err == model.ErrNotFound {
		loggerWithExecution(ctx, log.Fields{}).Debug("publication summary not found; initializing empty summary")
		return &model.PublicationSummary{}, nil
	}
	return nil, err
}
