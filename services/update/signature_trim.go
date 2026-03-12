package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/internal/sponsorblock"
	"github.com/mxpv/podsync/pkg/audiosig"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

type matchedRule struct {
	rule   SignatureRule
	result audiosig.Result
}

// trimEpisodeIfSignatureFound detects configured trim segments and applies them in one trim plan.
// Inputs:
// - ctx: context for cancellation.
// - feedConfig: feed configuration (uses feed ID for signature directory).
// - episode: episode metadata.
// - source: reader for downloaded episode file.
// Outputs:
// - reader: original or trimmed reader for upload.
// - cleanup: optional cleanup for temp files.
// - err: error if trimming fails.
// Example usage:
//
//	reader, cleanup, err := u.trimEpisodeIfSignatureFound(ctx, feedConfig, episode, tempFile)
//
// Notes: Returns original source if no signature file found.
func (u *Manager) trimEpisodeIfSignatureFound(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, source io.Reader) (io.Reader, func(), error) {
	if episode == nil || feedConfig == nil {
		return source, nil, nil
	}
	if u.sigDir == "" && !feedConfig.Custom.SponsorBlockConfig().Enabled {
		return source, nil, nil
	}
	logger := log.WithFields(log.Fields{"feed_id": feedConfig.ID, "episode_id": episode.ID, "signatures_root": u.sigDir})

	inputPath, inputBytes, inputCleanup, reusedSource, err := materializeTrimInput(source)
	if err != nil {
		return nil, nil, err
	}
	logger.WithFields(log.Fields{"input_path": inputPath, "input_bytes": inputBytes, "reused_source": reusedSource}).Info("[trim] Prepared source media for trim planning")

	matches, inputDur, err := u.collectTrimMatches(ctx, feedConfig, episode, inputPath, logger)
	if err != nil {
		if inputCleanup != nil {
			inputCleanup()
		}
		return nil, nil, err
	}
	if len(matches) == 0 {
		logger.WithField("input_bytes", inputBytes).Info("[trim] No trim operations configured or matched; skipping trim")
		if reusedSource {
			return source, inputCleanup, nil
		}
		file, err := os.Open(inputPath)
		if err != nil {
			if inputCleanup != nil {
				inputCleanup()
			}
			return nil, nil, fmt.Errorf("open input: %w", err)
		}
		cleanup := func() {
			_ = file.Close()
			if inputCleanup != nil {
				inputCleanup()
			}
		}
		return file, cleanup, nil
	}

	logger.WithFields(log.Fields{"matched_rules": len(matches), "input_bytes": inputBytes, "input_duration": inputDur}).Info("[trim] Applying planned trim rules")
	newInput, newCleanup, err := u.applyMatchedRules(ctx, inputPath, inputDur, matches, logger)
	if err != nil {
		if inputCleanup != nil {
			inputCleanup()
		}
		return nil, nil, err
	}
	file, err := os.Open(newInput)
	if err != nil {
		newCleanup()
		if inputCleanup != nil {
			inputCleanup()
		}
		return nil, nil, fmt.Errorf("open final input: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		newCleanup()
		if inputCleanup != nil {
			inputCleanup()
		}
	}
	return file, cleanup, nil
}

func materializeTrimInput(source io.Reader) (string, int64, func(), bool, error) {
	if named, ok := source.(interface{ Name() string }); ok {
		path := named.Name()
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, info.Size(), nil, true, nil
		}
	}
	tmpIn, err := os.CreateTemp("", "podsync-episode-*.bin")
	if err != nil {
		return "", 0, nil, false, fmt.Errorf("create temp input: %w", err)
	}
	inputBytes, err := io.Copy(tmpIn, source)
	if err != nil {
		_ = tmpIn.Close()
		_ = os.Remove(tmpIn.Name())
		return "", 0, nil, false, fmt.Errorf("write temp input: %w", err)
	}
	if err := tmpIn.Close(); err != nil {
		_ = os.Remove(tmpIn.Name())
		return "", 0, nil, false, fmt.Errorf("close temp input: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(tmpIn.Name())
	}
	return tmpIn.Name(), inputBytes, cleanup, false, nil
}

func (u *Manager) collectTrimMatches(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, inputPath string, logger log.FieldLogger) ([]matchedRule, time.Duration, error) {
	var (
		matches  []matchedRule
		inputDur time.Duration
	)

	signatureMatches, signatureDur, err := u.collectSignatureMatches(ctx, feedConfig, inputPath, logger)
	if err != nil {
		return nil, 0, err
	}
	if signatureDur > 0 {
		inputDur = signatureDur
	}
	matches = append(matches, signatureMatches...)

	sponsorMatches, err := u.collectSponsorBlockMatches(ctx, feedConfig, episode, logger)
	if err != nil {
		return nil, 0, err
	}
	matches = append(matches, sponsorMatches...)

	return matches, inputDur, nil
}

func (u *Manager) collectSignatureMatches(ctx context.Context, feedConfig *feed.Config, inputPath string, logger log.FieldLogger) ([]matchedRule, time.Duration, error) {
	if u.sigDir == "" {
		return nil, 0, nil
	}
	sigDir := filepath.Join(u.sigDir, feedConfig.ID, "signatures")
	rulesPath := filepath.Join(sigDir, "rules.json")
	rules, ok, err := ReadSignatureRules(rulesPath)
	if err != nil {
		return nil, 0, err
	}
	if !ok || len(rules.Rules) == 0 {
		logger.Info("[trim] No signature trim rules configured")
		return nil, 0, nil
	}
	logger.WithFields(log.Fields{"rules_path": rulesPath, "rules": len(rules.Rules)}).Info("[trim] Loaded signature trim rules")

	cfg := audiosig.Config{
		CoarseSampleRate: 4000,
		RefineSampleRate: 11025,
		EnvFPS:           25,
		RefineEnvFPS:     25,
		Margin:           15 * time.Second,
		FinalMargin:      750 * time.Millisecond,
		ExtraPad:         0,
		TopK:             5,
		MinScore:         0.6,
		MinPeakRatio:     1.2,
	}
	logger = logger.WithField("input", inputPath)
	logger.Debug("[trim] Signature detection started")
	var detected []matchedRule
	var inputDur time.Duration
	for idx, rule := range rules.Rules {
		if rule.File == "" || rule.Action == "" {
			logger.WithField("rule_index", idx).Debug("[trim] Invalid rule; skipping")
			continue
		}
		sigPath := filepath.Join(sigDir, rule.File)
		if info, err := os.Stat(sigPath); err != nil {
			if os.IsNotExist(err) {
				logger.WithFields(log.Fields{
					"rule_index": idx,
					"rule_file":  rule.File,
					"signature":  sigPath,
				}).Debug("[trim] Signature file missing; skipping rule")
				continue
			}
			return nil, 0, fmt.Errorf("stat signature file: %w", err)
		} else if info.Size() == 0 {
			logger.WithFields(log.Fields{
				"rule_index": idx,
				"rule_file":  rule.File,
				"signature":  sigPath,
			}).Debug("[trim] Signature file empty; skipping rule")
			continue
		}
		logger.WithFields(log.Fields{
			"rule_index":  idx,
			"rule_file":   rule.File,
			"rule_action": rule.Action,
			"rule_pre":    rule.PreSeconds,
			"rule_post":   rule.PostSeconds,
			"signature":   sigPath,
		}).Debug("[trim] Evaluating trim rule")

		logger = logger.WithField("input", inputPath)
		logger.Debug("[trim] Signature detection started")
		result, err := audiosig.Detect(ctx, inputPath, sigPath, cfg)
		if err != nil {
			return nil, 0, fmt.Errorf("signature detect failed: %w", err)
		}
		if !result.MatchFound {
			logger.WithField("rule_index", idx).Debug("[trim] Signature not detected for rule")
			continue
		}
		if inputDur == 0 {
			inputDur = result.InputDuration
		}
		logger = logger.WithFields(log.Fields{
			"signature_start": result.SignatureStart,
			"signature_end":   result.SignatureEnd,
			"split_at":        result.SplitAt,
			"confidence":      result.ConfidenceScore,
		})
		logger.WithFields(log.Fields{
			"rule_index":      idx,
			"rule_action":     rule.Action,
			"signature_start": result.SignatureStart,
			"signature_end":   result.SignatureEnd,
			"split_at":        result.SplitAt,
			"confidence":      result.ConfidenceScore,
		}).Info("[trim] Signature match found")
		detected = append(detected, matchedRule{rule: rule, result: result})
	}
	if len(detected) == 0 {
		logger.Info("[trim] No matching signature trim rules found")
	}
	return detected, inputDur, nil
}

func (u *Manager) collectSponsorBlockMatches(ctx context.Context, feedConfig *feed.Config, episode *model.Episode, logger log.FieldLogger) ([]matchedRule, error) {
	config := feedConfig.Custom.SponsorBlockConfig()
	if !config.Enabled {
		return nil, nil
	}
	logger.WithFields(log.Fields{"feed_id": feedConfig.ID, "categories": config.Categories}).Info("SponsorBlock enabled for feed")
	videoID := sponsorBlockVideoID(episode)
	if videoID == "" {
		logger.Warn("SponsorBlock video ID unavailable; skipping")
		return nil, nil
	}
	client := sponsorblock.NewClient(&http.Client{Timeout: 10 * time.Second})
	segments, err := client.SkipSegments(ctx, videoID)
	if err != nil {
		logger.WithFields(log.Fields{"video_id": videoID}).WithError(err).Warn("SponsorBlock request failed; continuing without SponsorBlock trimming")
		return nil, nil
	}
	logger.WithFields(log.Fields{"video_id": videoID, "segments": len(segments)}).Info("Retrieved SponsorBlock segments")
	selected := sponsorblock.FilterSegments(segments, config.Categories)
	logger.WithFields(log.Fields{"video_id": videoID, "categories": config.Categories, "segments": len(selected)}).Info("SponsorBlock segments selected for trimming")
	if len(selected) == 0 {
		return nil, nil
	}
	removeRanges := make([]timeRange, 0, len(selected))
	for _, segment := range selected {
		removeRanges = append(removeRanges, timeRange{start: segment.Start, end: segment.End})
	}
	merged := mergeRanges(removeRanges)
	result := make([]matchedRule, 0, len(merged))
	for _, remove := range merged {
		result = append(result, matchedRule{
			rule:   SignatureRule{Action: "remove_segment"},
			result: audiosig.Result{SignatureStart: remove.start, SignatureEnd: remove.end},
		})
	}
	return result, nil
}

func sponsorBlockVideoID(episode *model.Episode) string {
	if episode == nil {
		return ""
	}
	if strings.TrimSpace(episode.ID) != "" {
		return strings.TrimSpace(episode.ID)
	}
	if episode.VideoURL == "" {
		return ""
	}
	regex := regexp.MustCompile(`/v([a-z0-9]+)`)
	matches := regex.FindStringSubmatch(strings.ToLower(episode.VideoURL))
	if len(matches) < 2 {
		return ""
	}
	return "v" + matches[1]
}
